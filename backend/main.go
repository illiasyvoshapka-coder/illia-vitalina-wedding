package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
)

// FormResponse matches the JSON built in ilya-vitalina (1).html submitForm().
type FormResponse struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Attend  string `json:"attend"`
	PlusOne string `json:"plusOne"`
	Alco    string `json:"alco"`
	Tracks  string `json:"tracks"`
	Date    string `json:"date"`
}

// weddingItem is stored in DynamoDB. Partition key id (String) is generated on the server.
type weddingItem struct {
	ID      string `dynamodbav:"id"`
	Name    string `dynamodbav:"name"`
	Attend  string `dynamodbav:"attend"`
	PlusOne string `dynamodbav:"plusOne"`
	Alco    string `dynamodbav:"alco"`
	Tracks  string `dynamodbav:"tracks"`
	Date    string `dynamodbav:"date"`
}

var (
	ddbOnce sync.Once
	ddbCli  *dynamodb.Client
	ddbErr  error
)

func dynamoClient(ctx context.Context) (*dynamodb.Client, error) {
	ddbOnce.Do(func() {
		var cfg aws.Config
		cfg, ddbErr = config.LoadDefaultConfig(ctx)
		if ddbErr != nil {
			return
		}
		ddbCli = dynamodb.NewFromConfig(cfg)
	})
	if ddbErr != nil {
		return nil, ddbErr
	}
	return ddbCli, nil
}

func tableName() string {
	if t := os.Getenv("DYNAMODB_TABLE"); t != "" {
		return t
	}
	return "wedding"
}

func saveToDynamoDB(ctx context.Context, item weddingItem) error {
	cli, err := dynamoClient(ctx)
	if err != nil {
		return err
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	_, err = cli.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableName()),
		Item:      av,
	})
	return err
}

func corsHeaders() map[string]string {
	origin := os.Getenv("CORS_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return map[string]string{
		"Content-Type":                "application/json; charset=utf-8",
		"Access-Control-Allow-Origin": origin,
		"Access-Control-Allow-Headers": strings.Join([]string{
			"Content-Type",
			"Authorization",
		}, ", "),
		"Access-Control-Allow-Methods": "OPTIONS,POST",
	}
}

func jsonResponse(status int, body any) events.APIGatewayProxyResponse {
	b, err := json.Marshal(body)
	if err != nil {
		log.Printf("marshal response: %v", err)
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Headers:    corsHeaders(),
			Body:       `{"ok":false,"error":"internal"}`,
		}
	}
	return events.APIGatewayProxyResponse{
		StatusCode: status,
		Headers:    corsHeaders(),
		Body:       string(b),
	}
}

func decodeBody(req events.APIGatewayProxyRequest) (string, error) {
	body := req.Body
	if req.IsBase64Encoded {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return "", err
		}
		body = string(raw)
	}
	return body, nil
}

func handle(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	if strings.EqualFold(req.HTTPMethod, "OPTIONS") {
		return events.APIGatewayProxyResponse{
			StatusCode: 204,
			Headers:    corsHeaders(),
			Body:       "",
		}, nil
	}

	if !strings.EqualFold(req.HTTPMethod, "POST") {
		return jsonResponse(405, map[string]any{"ok": false, "error": "method not allowed"}), nil
	}

	raw, err := decodeBody(req)
	if err != nil {
		log.Printf("decode body: %v", err)
		return jsonResponse(400, map[string]any{"ok": false, "error": "invalid body encoding"}), nil
	}

	var payload FormResponse
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		log.Printf("json: %v", err)
		return jsonResponse(400, map[string]any{"ok": false, "error": "invalid json"}), nil
	}

	payload.Name = strings.TrimSpace(payload.Name)
	payload.PlusOne = strings.TrimSpace(payload.PlusOne)
	payload.Alco = strings.TrimSpace(payload.Alco)
	payload.Tracks = strings.TrimSpace(payload.Tracks)
	payload.Date = strings.TrimSpace(payload.Date)

	if payload.Name == "" {
		return jsonResponse(400, map[string]any{"ok": false, "error": "name is required"}), nil
	}
	if payload.Attend != "yes" && payload.Attend != "no" {
		return jsonResponse(400, map[string]any{"ok": false, "error": "attend must be \"yes\" or \"no\""}), nil
	}

	rowID := uuid.NewString()
	item := weddingItem{
		ID:      rowID,
		Name:    payload.Name,
		Attend:  payload.Attend,
		PlusOne: payload.PlusOne,
		Alco:    payload.Alco,
		Tracks:  payload.Tracks,
		Date:    payload.Date,
	}

	if err := saveToDynamoDB(ctx, item); err != nil {
		log.Printf("dynamodb PutItem: %v", err)
		return jsonResponse(502, map[string]any{"ok": false, "error": "failed to save"}), nil
	}

	log.Printf("rsvp saved id=%s name=%q attend=%s", rowID, payload.Name, payload.Attend)

	return jsonResponse(200, map[string]any{"ok": true, "id": rowID}), nil
}

func main() {
	lambda.Start(handle)
}
