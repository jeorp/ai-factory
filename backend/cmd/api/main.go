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
	providers := map[string]agent.Provider{}
	for _, name := range c.ModelProviders {
		switch name {
		case "demo":
			providers[name] = agent.Demo{}
		case "openai":
			providers[name] = &agent.OpenAI{HTTP: client}
		case "gemini":
			providers[name] = &agent.Gemini{HTTP: client}
		case "vertex":
			// The SDK's own client has no overall timeout; the run deadline bounds streamed answers.
			providers[name] = &agent.Claude{Vertex: true, Project: c.VertexProject, Region: c.VertexRegion}
		case "anthropic":
			providers[name] = &agent.Claude{}
		}
	}
	ai := agent.ModelRouter{Providers: providers, ModelProviders: c.ModelProviders}
	ciWait := 5 * time.Minute
	if c.Demo {
		ciWait = 0
	}
	api := httpapi.New(c, auth, &store.Store{DB: db}, gh, &agent.Runner{AI: ai, GitHub: gh, Writer: gh, CIWait: ciWait}, client)
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
