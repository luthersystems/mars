package main

import (
	"context"
	"fmt"
	"os"

	"github.com/luthersystems/mars/internal/secretstore"
	"github.com/luthersystems/mars/internal/vaulthelper"
)

func main() {
	values, err := vaulthelper.RequiredEnv(os.Getenv, "AZ_KEYVAULT_NAME", "AZ_KEYVAULT_KEY")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	secret, err := secretstore.AzureSecret(context.Background(), values["AZ_KEYVAULT_NAME"], values["AZ_KEYVAULT_KEY"])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := vaulthelper.PrintSecret(os.Stdout, secret); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
