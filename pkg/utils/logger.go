package utils

import (
	"log"
	"os"
)

// Logger простой логгер (в будущем можно заменить на zap/zerolog)
type Logger struct {
	*log.Logger
}

func NewLogger() *Logger {
	return &Logger{
		Logger: log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile),
	}
}

func (l *Logger) Info(msg string, args ...interface{}) {
	l.Printf("[INFO] "+msg, args...)
}

func (l *Logger) Error(msg string, args ...interface{}) {
	l.Printf("[ERROR] "+msg, args...)
}

func (l *Logger) Debug(msg string, args ...interface{}) {
	l.Printf("[DEBUG] "+msg, args...)
}

func (l *Logger) Warn(msg string, args ...interface{}) {
	l.Printf("[WARN] "+msg, args...)
}
