package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/saidovux/remna-quota/internal/backend"
)

func main() {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "keygen" {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			fmt.Fprintln(os.Stderr, "key generation failed")
			os.Exit(1)
		}
		fmt.Println(base64.RawURLEncoding.EncodeToString(key[:]))
		return
	}
	if command != "serve" && command != "check-config" && command != "check-provider" {
		fmt.Fprintln(os.Stderr, "usage: quota-api [serve|check-config|check-provider|keygen]")
		os.Exit(2)
	}
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "cannot read .env")
		os.Exit(1)
	}
	cfg, err := backend.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if command == "check-config" {
		fmt.Println("configuration=valid provider=" + cfg.Provider)
		return
	}
	if command == "check-provider" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		report, err := backend.CheckProvider(ctx, cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			os.Exit(1)
		}
		if !report.Ready {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := backend.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
