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

	if !strings.EqualFold(method, "POST") {
		log.Printf("reject method=%q (expected POST)", method)
		return jsonResponse(405, map[string]any{"ok": false, "error": "method not allowed"}), nil
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

// handle unmarshals either REST API (v1) or HTTP API (v2) payloads. Using only
// APIGatewayProxyRequest breaks HTTP API: httpMethod lives under requestContext.http.method.
func handle(ctx context.Context, raw json.RawMessage) (events.APIGatewayProxyResponse, error) {
	var probe struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		log.Printf("probe event: %v", err)
		return jsonResponse(500, map[string]any{"ok": false, "error": "bad event"}), nil
	}

	if probe.Version == "2.0" {
		var req events.APIGatewayV2HTTPRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			log.Printf("apigw v2 unmarshal: %v", err)
			return jsonResponse(500, map[string]any{"ok": false, "error": "bad event"}), nil
		}
		method := req.RequestContext.HTTP.Method
		log.Printf("apigw v2 method=%q route=%q rawPath=%q body_len=%d", method, req.RouteKey, req.RawPath, len(req.Body))
		body, err := decodeRawBody(req.Body, req.IsBase64Encoded)
		if err != nil {
			log.Printf("decode body: %v", err)
			return jsonResponse(400, map[string]any{"ok": false, "error": "invalid body encoding"}), nil
		}
		return coreHandler(ctx, method, body)
	}

	var req events.APIGatewayProxyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		log.Printf("apigw v1 unmarshal: %v", err)
		return jsonResponse(500, map[string]any{"ok": false, "error": "bad event"}), nil
	}
	log.Printf("apigw v1 method=%q path=%q body_len=%d", req.HTTPMethod, req.Path, len(req.Body))
	body, err := decodeRawBody(req.Body, req.IsBase64Encoded)
	if err != nil {
		log.Printf("decode body: %v", err)
		return jsonResponse(400, map[string]any{"ok": false, "error": "invalid body encoding"}), nil
	}
	return coreHandler(ctx, req.HTTPMethod, body)
}

func main() {
	lambda.Start(handle)
}
