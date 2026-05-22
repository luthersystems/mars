package main

import (
	"context"
	"fmt"
	"os"

	"github.com/luthersystems/mars/internal/secretstore"
	"github.com/luthersystems/mars/internal/vaulthelper"
)

func main() {
	values, err := vaulthelper.RequiredEnv(os.Getenv, "AWS_SM_SECRET_ID", "AWS_REGION")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	secret, err := secretstore.AWSSecret(context.Background(), values["AWS_SM_SECRET_ID"], values["AWS_REGION"], os.Getenv("AWS_ROLE_ARN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := vaulthelper.PrintSecret(os.Stdout, secret); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
