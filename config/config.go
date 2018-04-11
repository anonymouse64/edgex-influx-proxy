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

// TomlConfigTree is a simple wrapper function to get the toml tree from a config file
func TomlConfigTree(tomlFile string) (*toml.Tree, error) {
	return toml.LoadFile(tomlFile)
}

// TomlConfigKeys returns all toml keys in the config struct
func TomlConfigKeys(tree *toml.Tree) []string {
	leaveNames := make([]string, 0, 100)
	recurseLeaves(tree, "", &leaveNames)
	return leaveNames
}

// recurseLeaves follows all leaves of a toml.Tree, getting all possible key values
// that can be used
func recurseLeaves(tree *toml.Tree, prefix string, leaves *[]string) {
	// Iterate over all branches of this tree, checking if each branch is a leaf
	// or a subtree, recursing on subtrees
	for _, branchName := range tree.Keys() {
		branch := tree.Get(branchName)
		if subtree, ok := branch.(*toml.Tree); !ok {
			// This branch is a leaf - add it to the list of leaves
			leavesSlice := *leaves
			*leaves = append(leavesSlice, prefix+"."+branchName)
		} else {
			// This branch has more leaves - recurse into it
			if prefix == "" {
				// Don't include the prefix - this is the first call
				recurseLeaves(subtree, branchName, leaves)
			} else {
				// Include the prefix - this is a recursed call
				recurseLeaves(subtree, prefix+"."+branchName, leaves)
			}
		}
	}
}

func SetTreeValues(valmap map[string]interface{}, tree *toml.Tree) (*ServerConfig, error) {
	// iterate over the values, setting them inside the tree
	for key, val := range valmap {
		tree.Set(key, val)
	}

	// marshal the tree to toml bytes, then unmarshal the bytes into the struct
	var cfg ServerConfig
	treeString, err := tree.ToTomlString()
	if err != nil {
		return nil, err
	}
	err = toml.Unmarshal([]byte(treeString), &cfg)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
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
