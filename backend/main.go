package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"sort"
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

func decodeRawBody(body string, isBase64 bool) (string, error) {
	if !isBase64 {
		return body, nil
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// coreHandler runs after API Gateway event is normalized (method + JSON body).
func coreHandler(ctx context.Context, method, rawBody string) (events.APIGatewayProxyResponse, error) {
	if strings.EqualFold(method, "OPTIONS") {
		return events.APIGatewayProxyResponse{
			StatusCode: 204,
			Headers:    corsHeaders(),
			Body:       "",
		}, nil
	}

	var payload FormResponse
	if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
		log.Printf("json: %v body_prefix=%q", err, trimForLog(rawBody, 200))
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

func trimForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// apigwFlexible unmarshals both HTTP API (v2) and REST proxy (v1) payloads: v1 uses
// top-level httpMethod/path; v2 uses rawPath and requestContext.http.{method,path}.
// Body is json.RawMessage so a JSON string body does not fail unmarshaling when we
// also need to accept object-shaped bodies from some HTTP triggers (see bodyFromEvent).
type apigwFlexible struct {
	Body            json.RawMessage `json:"body"`
	IsBase64Encoded bool            `json:"isBase64Encoded"`
	HTTPMethod      string          `json:"httpMethod"`
	Path            string          `json:"path"`
	RawPath         string          `json:"rawPath"`
	RequestContext  struct {
		HTTP struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"http"`
	} `json:"requestContext"`
}

func normalizeMethodPath(ev *apigwFlexible) (method, path string) {
	method = ev.RequestContext.HTTP.Method
	if method == "" {
		method = ev.HTTPMethod
	}
	path = ev.RawPath
	if path == "" {
		path = ev.RequestContext.HTTP.Path
	}
	if path == "" {
		path = ev.Path
	}
	return method, path
}

// bodyFromEvent returns the HTTP body before base64 decoding. API Gateway sends body
// as a JSON string; some platforms send a JSON object (same shape as FormResponse).
func bodyFromEvent(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func looksLikeFormPayload(b []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	_, hasName := m["name"]
	_, hasAttend := m["attend"]
	return hasName && hasAttend
}

func topLevelKeysJSON(b []byte) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func handle(ctx context.Context, payload json.RawMessage) (events.APIGatewayProxyResponse, error) {
	if os.Getenv("WEDDING_DEBUG_PAYLOAD") == "1" {
		log.Printf("payload len=%d keys=%v prefix=%s", len(payload), topLevelKeysJSON(payload), trimForLog(string(payload), 800))
	}

	var ev apigwFlexible
	if err := json.Unmarshal(payload, &ev); err != nil {
		log.Printf("apigw unmarshal: %v", err)
		return jsonResponse(500, map[string]any{"ok": false, "error": "bad event"}), nil
	}

	method, path := normalizeMethodPath(&ev)
	bodyStr := bodyFromEvent(ev.Body)
	isB64 := ev.IsBase64Encoded

	// Non-proxy or test events: whole event is the RSVP JSON (name + attend at top level).
	if strings.TrimSpace(bodyStr) == "" && looksLikeFormPayload(payload) {
		bodyStr = string(bytes.TrimSpace(payload))
		if method == "" {
			method = "POST"
		}
		log.Printf("apigw: envelope had empty body; using top-level JSON as RSVP body")
	}

	log.Printf("apigw method=%q path=%q body_len=%d isB64=%v", method, path, len(bodyStr), isB64)

	decoded, err := decodeRawBody(bodyStr, isB64)
	if err != nil {
		log.Printf("decode body: %v", err)
		return jsonResponse(400, map[string]any{"ok": false, "error": "invalid body encoding"}), nil
	}

	return coreHandler(ctx, method, decoded)
}

func main() {
	lambda.Start(handle)
}
