package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const apiToken = ""

var (
	addBirthdayHint = `Напишите День рождения в следующем формате:
'Имя' 'Дата Рождения'
Например:
Илья 26.03.2003
Также можно указать фамилию или не указывать год рождения:
Илья Сивошапка 26.03`
	deleteBirthdayHint = `Укажите имя Дня рождения которое вы хотите удалить`
)

type Input struct {
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

type Output struct {
	IsBase64Encoded bool              `json:"isBase64Encoded"`
	StatusCode      int               `json:"statusCode"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
}

type Handler struct {
	birthdayDB  *BirthdayDatabase
	userStateDB *UserStateDatabase
}

func (h Handler) Invoke(ctx context.Context, lambdaInput []byte) ([]byte, error) {
	var input Input
	err := json.Unmarshal(lambdaInput, &input)
	if err != nil {
		log.Println(fmt.Sprintf("error: %s, input: %s", err, lambdaInput))
		return response(http.StatusBadRequest)
	}

	var payload tgbotapi.Update
	err = json.Unmarshal([]byte(input.Body), &payload)
	if err != nil {
		log.Println(fmt.Sprintf("error: %s, body: %s", err, input.Body))
		return response(http.StatusBadRequest)
	}

	if payload.Message == nil || payload.Message.Chat == nil {
		log.Println("message or chat is nil")
		return response(http.StatusBadRequest)
	}

	bot, err := tgbotapi.NewBotAPI(apiToken)
	if err != nil {
		log.Println(fmt.Sprintf("error creating bot: %s", err))
		return response(http.StatusInternalServerError)
	}

	// Handle different flows based on user state
	currentState, err := h.userStateDB.GetUserState(ctx, payload.Message.Chat.ID)
	if err != nil {
		log.Println(err)
		return response(http.StatusInternalServerError)
	}

	// Handle commands
	if payload.Message.Command() != "" {
		return h.handleCommand(ctx, bot, payload.Message)
	}

	switch currentState {
	case "add_birthday":
		return h.handleAddBirthdayFlow(ctx, bot, payload.Message)
	case "delete_birthday":
		return h.handleDeleteBirthdayFlow(ctx, bot, payload.Message)
	}

	// Regular put item for chat tracking
	err = h.userStateDB.SetUserState(context.Background(), payload.Message.Chat.ID, "")
	if err != nil {
		log.Println(err)
		return response(http.StatusInternalServerError)
	}

	return response(http.StatusOK)
}

// handleCommand handles different bot commands
func (h Handler) handleCommand(ctx context.Context, bot *tgbotapi.BotAPI, message *tgbotapi.Message) ([]byte, error) {
	var responseMessage string
	switch message.Command() {
	case "add_birthday":
		// Set user state to add_birthday
		err := h.userStateDB.SetUserState(ctx, message.Chat.ID, "add_birthday")
		if err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}

		if _, err := bot.Send(tgbotapi.NewMessage(message.Chat.ID, addBirthdayHint)); err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}
	case "delete_birthday":
		// Set user state to delete_birthday
		err := h.userStateDB.SetUserState(ctx, message.Chat.ID, "delete_birthday")
		if err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}

		if _, err := bot.Send(tgbotapi.NewMessage(message.Chat.ID, deleteBirthdayHint)); err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}
	case "list_birthdays":
		err := h.userStateDB.SetUserState(ctx, message.Chat.ID, "")
		if err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}

		birthdays, err := h.birthdayDB.GetAllBirthdays(ctx, message.Chat.ID)
		if err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}

		for _, item := range birthdays {
			responseMessage = responseMessage + "\n" + fmt.Sprintf("%s %s", item.Name, item.Date)
		}

		if responseMessage == "" {
			responseMessage = "Список дней рождений пуст"
		}

		if _, err := bot.Send(tgbotapi.NewMessage(message.Chat.ID, responseMessage)); err != nil {
			log.Println(err)
			return response(http.StatusInternalServerError)
		}
	}
	return response(http.StatusOK)
}

// parseBirthdayInput parses user input for birthday format
func parseBirthdayInput(input string) (string, string, error) {
	// Split by spaces
	parts := strings.Fields(input)

	if len(parts) < 2 {
		return "", "", fmt.Errorf("invalid format")
	}

	// Extract date (last part)
	dateStr := parts[len(parts)-1]

	// Extract name (everything except last part)
	nameParts := parts[:len(parts)-1]
	name := strings.Join(nameParts, " ")

	dateStr, err := validateAndFormatDate(dateStr)
	if err != nil {
		return "", "", err
	}

	return name, dateStr, nil
}

func validateAndFormatDate(dateStr string) (string, error) {
	// Try different date formats with and without leading zeros
	formats := []string{
		"02.01.2006", // DD.MM.YYYY (with leading zeros)
		"2.01.2006",  // D.MM.YYYY (without leading zero for day)
		"02.1.2006",  // DD.M.YYYY (without leading zero for month)
		"2.1.2006",   // D.M.YYYY (without leading zeros)
		"02.01",      // DD.MM (with leading zeros)
		"2.01",       // D.MM (without leading zero для day)
		"02.1",       // DD.M (without leading zero для month)
		"2.1",        // D.M (without leading zeros)
	}

	dotNumber := strings.Count(dateStr, ".")

	for _, format := range formats {
		if len(dateStr) == len(format) {
			date, err := time.Parse(format, dateStr)
			if err == nil {
				if dotNumber == 2 {
					return date.Format("02.01.2006"), nil
				}

				return date.Format("02.01"), nil
			}
		}
	}

	return "", fmt.Errorf("invalid date format")
}

// handleAddBirthdayFlow handles the add birthday flow
func (h Handler) handleAddBirthdayFlow(ctx context.Context, bot *tgbotapi.BotAPI, message *tgbotapi.Message) ([]byte, error) {
	// Parse the birthday input
	name, date, err := parseBirthdayInput(message.Text)
	if err != nil {
		return h.sendErrorMessage(bot, message.Chat.ID, "Неверный формат. Пожалуйста, используйте формат: Имя [Фамилия] ДД.ММ[.ГГГГ]")
	}

	// Check if name is unique
	isUnique, err := h.birthdayDB.IsNameUnique(ctx, message.Chat.ID, name)
	if err != nil {
		return h.handleDatabaseError(err)
	}

	if !isUnique {
		return h.sendErrorMessage(bot, message.Chat.ID, fmt.Sprintf("День рождения с именем '%s' уже существует. Пожалуйста, используйте другое имя.", name))
	}

	// Add birthday to database
	err = h.birthdayDB.AddBirthday(ctx, message.Chat.ID, name, date)
	if err != nil {
		return h.handleDatabaseError(err)
	}

	// Send success message and clear state
	successMessage := fmt.Sprintf("День рождения добавлен: %s %s", name, date)
	return h.sendSuccessMessageAndClearState(ctx, bot, message.Chat.ID, successMessage)
}

// handleDeleteBirthdayFlow handles the delete birthday flow
func (h Handler) handleDeleteBirthdayFlow(ctx context.Context, bot *tgbotapi.BotAPI, message *tgbotapi.Message) ([]byte, error) {
	name := strings.TrimSpace(message.Text)

	// Check if birthday exists
	birthday, err := h.birthdayDB.GetBirthdayByName(ctx, message.Chat.ID, name)
	if err != nil {
		return h.handleDatabaseError(err)
	}

	if birthday == nil {
		return h.sendErrorMessage(bot, message.Chat.ID, fmt.Sprintf("День рождения с именем '%s' не найдено.", name))
	}

	// Delete birthday
	err = h.birthdayDB.DeleteBirthday(ctx, message.Chat.ID, name)
	if err != nil {
		return h.handleDatabaseError(err)
	}

	// Send success message and clear state
	successMessage := fmt.Sprintf("День рождения удален: %s", name)
	return h.sendSuccessMessageAndClearState(ctx, bot, message.Chat.ID, successMessage)
}

// sendErrorMessage sends an error message to the user and returns an error response
func (h Handler) sendErrorMessage(bot *tgbotapi.BotAPI, chatID int64, message string) ([]byte, error) {
	if _, err := bot.Send(tgbotapi.NewMessage(chatID, message)); err != nil {
		log.Println(err)
		return response(http.StatusInternalServerError)
	}
	return response(http.StatusOK)
}

// handleDatabaseError logs the error and returns an internal server error response
func (h Handler) handleDatabaseError(err error) ([]byte, error) {
	log.Println(err)
	return response(http.StatusInternalServerError)
}

// sendSuccessMessageAndClearState sends a success message and clears the user state
func (h Handler) sendSuccessMessageAndClearState(ctx context.Context, bot *tgbotapi.BotAPI, chatID int64, message string) ([]byte, error) {
	if _, err := bot.Send(tgbotapi.NewMessage(chatID, message)); err != nil {
		log.Println(err)
		return response(http.StatusInternalServerError)
	}

	err := h.userStateDB.SetUserState(ctx, chatID, "")
	if err != nil {
		log.Println(err)
		return response(http.StatusInternalServerError)
	}

	return response(http.StatusOK)
}

func response(status int) ([]byte, error) {
	return json.Marshal(Output{IsBase64Encoded: false, StatusCode: status})
}
