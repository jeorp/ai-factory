package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/github"
	"ai-factory/backend/internal/httpapi"
	"ai-factory/backend/internal/store"
	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	c, err := httpapi.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: c.Project})
	if err != nil {
		log.Fatal("firebase initialization failed")
	}
	auth, err := app.Auth(ctx)
	if err != nil {
		log.Fatal("auth initialization failed")
	}
	db, err := firestore.NewClient(ctx, c.Project)
	if err != nil {
		log.Fatal("firestore initialization failed")
	}
	defer db.Close()
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	gh := &github.Client{HTTP: client, Demo: c.Demo, Cache: &github.FirestoreReadCache{DB: db}}
	var ai agent.Provider = &agent.OpenAI{HTTP: client}
	if c.AIProvider == "demo" {
		ai = agent.Demo{}
	} else if c.AIProvider == "gemini" {
		ai = &agent.Gemini{HTTP: client}
	}
	api := httpapi.New(c, auth, &store.Store{DB: db}, gh, &agent.Runner{AI: ai, GitHub: gh}, client)
	server := &http.Server{Addr: ":" + c.Port, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 * 1024}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		os.Exit(0)
	}()
	log.Printf("AI Factory API listening on :%s (demo=%t)", c.Port, c.Demo)
	if err = server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal("http server failed")
	}
}
