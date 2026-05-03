package config

import "github.com/kelseyhightower/envconfig"

// Load заполняет структуру c значениями из переменных окружения.
// Использует envconfig с пустым префиксом — ожидаются полные имена переменных.
func Load(c any) error { return envconfig.Process("", c) }
