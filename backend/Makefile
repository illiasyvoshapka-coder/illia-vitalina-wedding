# AWS Lambda (provided.al2 / provided.al2023): executable must be named "bootstrap"
.PHONY: build tidy zip

build:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap .

zip: build
	rm -f function.zip
	zip function.zip bootstrap

tidy:
	go mod tidy
