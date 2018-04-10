package config

import (
	"fmt"
	"os"

	toml "github.com/pelletier/go-toml"
)

type edgeXConfig struct {
	RegisterRESTClient bool   `toml:"register"`
	CleanRegistration  bool   `toml:"clean-register"`
	ExportDistroHost   string `toml:"exporthost"`
	ExportDistroPort   int    `toml:"exportport"`
}

type httpConfig struct {
	Port int    `toml:"port"`
	Host string `toml:"host"`
}

type influxConfig struct {
	Port        int    `toml:"port"`
	Host        string `toml:"host"`
	DBName      string `toml:"dbname"`
	DBPrecision string `toml:"dbprecision"`
}

// ServerConfig holds all of the config values
type ServerConfig struct {
	HTTPConfig   httpConfig   `toml:"http"`
	InfluxConfig influxConfig `toml:"influx"`
	EdgeXConfig  edgeXConfig  `toml:"edgex"`
}

// Config is the current server config
var Config = defaultConfig()

// LoadConfig will read in the file, loading the config, and perform validation on the config
func LoadConfig(file string) error {
	// open the file for reading
	f, err := os.Open(file)
	if err != nil {
		return err
	}

	// make a new decoder with the file
	// and decode the file into the global config
	err = toml.NewDecoder(f).Decode(Config)
	if err != nil {
		return err
	}

	// validate the config
	return checkConfig(Config)
}

// WriteConfig will write out the specified config to the file
func WriteConfig(file string, userconfig *ServerConfig) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	defer f.Close()

	// Check the specified config, if it is nil, then
	// use the global one
	var cfgToUse *ServerConfig
	if userconfig == nil {
		// use the global one
		cfgToUse = Config
	} else {
		cfgToUse = userconfig
	}

	// encode the config to the file
	return toml.NewEncoder(f).Encode(*cfgToUse)
}

// checks that the precision for the database is correctly specified
func validDBPrecision(prec string) bool {
	switch prec {
	case "ns",
		"us",
		"ms",
		"s",
		"":
		return true
	default:
		return false
	}
}

// checks various properties in the config to make sure they're usable
func checkConfig(cfg *ServerConfig) error {
	switch {
	// check that ports are greater than 0
	case cfg.HTTPConfig.Port < 1:
		return fmt.Errorf("http port %d is invalid", cfg.HTTPConfig.Port)
	case cfg.InfluxConfig.Port < 1:
		return fmt.Errorf("influx port %d is invalid", cfg.InfluxConfig.Port)
	// only check the edgex port if we need to register the server as a REST client
	case cfg.EdgeXConfig.RegisterRESTClient && cfg.EdgeXConfig.ExportDistroPort < 1:
		return fmt.Errorf("edgex export-distro port %d is invalid", cfg.EdgeXConfig.ExportDistroPort)
	// check the database name
	case cfg.InfluxConfig.DBName == "":
		return fmt.Errorf("influx dbname %s is invalid", cfg.InfluxConfig.DBName)
	// check the database precision
	case !validDBPrecision(cfg.InfluxConfig.DBPrecision):
		return fmt.Errorf("influx db precision %s is invalid", cfg.InfluxConfig.DBPrecision)
	default:
		return nil
	}
}

// default values for the config
func defaultConfig() *ServerConfig {
	return &ServerConfig{
		HTTPConfig: httpConfig{
			Port: 8080,
			Host: "",
		},
		InfluxConfig: influxConfig{
			Host:        "localhost",
			Port:        8086,
			DBName:      "edgex",
			DBPrecision: "ns",
		},
		EdgeXConfig: edgeXConfig{
			RegisterRESTClient: false,
			CleanRegistration:  true,
			ExportDistroHost:   "localhost",
			ExportDistroPort:   48071,
		},
	}
}
