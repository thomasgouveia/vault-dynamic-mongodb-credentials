package main

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func envOr(key string, defaultValue string) string {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	return v
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	ctx := context.Background()

	dbName := envOr("MONGODB_DATABASE", "demo-app")
	uri := envOr("MONGODB_URI", "")
	if uri == "" {
		log.Fatal("[ERROR]: MONGODB_URI is required.")
	}

	// Establish connection with MongoDB.
	opts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		log.Fatalf("[ERROR]: Failed to connect to MongoDB: %v", err)
	}

	r := gin.Default()

	r.GET("/_/health/liveness", func(c *gin.Context) {
		// Check that we can write into our database
		if _, err := client.Database(dbName).Collection("ping").InsertOne(c.Request.Context(), bson.M{"ping": true}); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "DOWN",
				"mongodb": map[string]any{
					"error": err.Error(),
				},
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "UP",
		})
	})

	r.GET("/_/health/readiness", func(c *gin.Context) {
		// Check that we can communicate with the server
		if err := client.Ping(c.Request.Context(), readpref.Primary()); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "DOWN",
				"mongodb": map[string]any{
					"error": err.Error(),
				},
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "UP",
		})
	})

	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"username": opts.Auth.Username,
			"password": opts.Auth.Password,
		})
	})

	s := &http.Server{
		Addr:    ":8080",
		Handler: r,
	}

	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[ERROR]: could not start app: %v", err)
		}
	}()

	log.Println("[INFO]: App is running on 0.0.0.0:8080")

	quit := make(chan os.Signal)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal to notify the channel
	<-quit
	log.Println("[INFO]: Exiting server gracefully")

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := s.Shutdown(ctx); err != nil {
		log.Fatalf("[ERROR]: server shutdown: %v", err)
	}

	if err := client.Disconnect(ctx); err != nil {
		log.Fatalf("[ERROR]: MongoDB disconnection: %v", err)
	}
}
