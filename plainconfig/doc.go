// Package plainconfig — alias-обёртка над `github.com/H-BF/corlib/pkg/plain-config`.
//
// Назначение: type-safe доступ к viper-конфигу с поддержкой YAML-файла,
// env-переменных, флагов и default-значений. Альтернатива
// `kacho-corelib/config` (который использует envconfig).
//
// Когда выбирать plainconfig vs config:
//   - config (envconfig)  — простые сервисы, всё через env-vars (текущий 0.x default)
//   - plainconfig (viper) — нужен YAML-файл, runtime-overrides через флаги, типизированные accessors
//
// Базовый pattern:
//
//	const (
//	    LoggerLevel    = plainconfig.ValueT[string]("logger.level")
//	    GrpcEndpoint   = plainconfig.ValueT[string]("server.grpc.endpoint")
//	    MetricsEnable  = plainconfig.ValueT[bool]("metrics.enable")
//	)
//
//	func main() {
//	    err := plainconfig.InitGlobalConfig(
//	        plainconfig.WithAcceptEnvironment{EnvPrefix: "KACHO_RESOURCE_MANAGER"},
//	        plainconfig.WithSourceFile{FileName: "/etc/kacho/resource-manager.yaml"},
//	        plainconfig.WithDefValue(LoggerLevel, "INFO"),
//	        plainconfig.WithDefValue(GrpcEndpoint, "tcp://0.0.0.0:9090"),
//	        plainconfig.WithDefValue(MetricsEnable, true),
//	    )
//	    if err != nil { log.Fatal(err) }
//
//	    level := LoggerLevel.MustValue(ctx)        // string
//	    enabled := MetricsEnable.MustValue(ctx)    // bool
//	}
//
// Полные docs upstream: https://github.com/H-BF/corlib/tree/master/pkg/plain-config
package plainconfig

import (
	pc "github.com/H-BF/corlib/pkg/plain-config"
)

// Re-export часто-используемых типов и функций.
//
// Использование: `import "github.com/PRO-Robotech/kacho-corelib/plainconfig"`
// затем `plainconfig.ValueT[T]`, `plainconfig.InitGlobalConfig(...)`, и т.д.
// Re-export не-generic символов как aliases.
type (
	Option                = pc.Option
	WithAcceptEnvironment = pc.WithAcceptEnvironment
	WithSourceFile        = pc.WithSourceFile
)

// InitGlobalConfig — re-export не-generic функции.
var InitGlobalConfig = pc.InitGlobalConfig

// Generic-функции (`WithDefValue`, `BindFlag`) и тип `ValueT[T]`
// невозможно re-export-нуть как value/alias в Go без instantiation, поэтому
// используй их через прямой импорт:
//
//	import config "github.com/H-BF/corlib/pkg/plain-config"
//
//	const LoggerLevel = config.ValueT[string]("logger.level")
//	plainconfig.InitGlobalConfig(
//	    plainconfig.WithAcceptEnvironment{EnvPrefix: "KACHO_RM"},
//	    config.WithDefValue(LoggerLevel, "INFO"),
//	)
