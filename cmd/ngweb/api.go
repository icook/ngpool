package main

import (
	"github.com/gin-gonic/gin"
	"github.com/icook/ngpool/pkg/service"
	log "github.com/inconshreveable/log15"
	"github.com/itsjamie/gin-cors"
	"github.com/jmoiron/sqlx"
	"github.com/spf13/viper"
	"gopkg.in/go-playground/validator.v9"
	"os"
	"time"
)

var validate = validator.New()

type NgWebAPI struct {
	log log.Logger

	config  *viper.Viper
	db      *sqlx.DB
	engine  *gin.Engine
	service *service.Service
}

func NewNgWebAPI() *NgWebAPI {
	var ngw = NgWebAPI{
		log: log.New(),
	}
	return &ngw
}

func (q *NgWebAPI) ParseConfig() {
	// Load our configuration info
	config := viper.New()
	config.SetConfigType("yaml")
	config.SetDefault("LogLevel", "info")
	config.SetDefault("DbConnectionString",
		"user=ngpool dbname=ngpool sslmode=disable password=knight")
	q.config = config
	q.service = service.NewService("api", config)
	q.service.SetLabels(map[string]interface{}{
		"endpoint": config.GetString("StratumBind"),
	})
	// TODO: Check for secure JWTSecret

	db, err := sqlx.Connect("postgres", config.GetString("DbConnectionString"))
	if err != nil {
		q.log.Crit("Failed connect db", "err", err)
		os.Exit(1)
	}
	q.db = db

	levelConfig := q.config.GetString("LogLevel")
	level, err := log.LvlFromString(levelConfig)
	if err != nil {
		log.Crit("Unable to parse log level", "configval", levelConfig, "err", err)
		os.Exit(1)
	}
	handler := log.CallerFileHandler(log.StdoutHandler)
	handler = log.LvlFilterHandler(level, handler)
	q.log.SetHandler(handler)
	log.Info("Set log level", "level", level)
}

func (q *NgWebAPI) SetupGin() {
	// Setup our database connection
	// Configure webserver
	r := gin.Default()
	r.Use(cors.Middleware(cors.Config{
		Origins:         "*",
		Methods:         "GET, PUT, POST, DELETE",
		RequestHeaders:  "Origin, Authorization, Content-Type",
		ExposedHeaders:  "",
		MaxAge:          50 * time.Second,
		Credentials:     true,
		ValidateHeaders: false,
	}))

	r.POST("/v1/register", q.postRegister)
	r.POST("/v1/login", q.postLogin)

	api := r.Group("/v1/user/")
	api.Use(q.authMiddleware)
	{
		api.POST("tfa", q.postTFA)
		api.POST("tfa_setup", q.postTFASetup)
		api.POST("setpayout", q.postSetPayout)

		api.GET("me", q.getMe)
	}

	q.engine = r
}
