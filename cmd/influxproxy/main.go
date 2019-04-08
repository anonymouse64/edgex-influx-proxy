package main

import (
	"fmt"
	"log"
	"math"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"os"

	tc "github.com/anonymouse64/configurator/tomlconfigurator"
	"github.com/edgexfoundry/go-mod-core-contracts/models"
	influx "github.com/influxdata/influxdb/client/v2"
	flags "github.com/jessevdk/go-flags"
	"github.com/pelletier/go-toml"
)

const (
	edgeXCreateRESTRegistrationJSON = `{
	"name":"golang-server",
	"enable":true,
	"format":"JSON",
	"destination": "REST_ENDPOINT",
	"addressable": {
	    "name": "DesktopREST",
	    "protocol": "HTTP",
   	    "method": "POST", 
	    "address": "%s",
	    "port": %d,
	    "path": "/edgex"
	  }
}`
	httpUnknownError = iota
	httpInvalidFormat
	httpInvalidName
	httpInvalidNumber
	httpInvalidReading
	httpPlotFailure
)

type dataValueType int

const (
	boolType dataValueType = iota
	intType
	floatType
	stringType
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
type serverConfig struct {
	HTTPConfig   httpConfig   `toml:"http"`
	InfluxConfig influxConfig `toml:"influx"`
	EdgeXConfig  edgeXConfig  `toml:"edgex"`
}

// Config is the current configuration for the server in memory
var Config *serverConfig

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
func (s *serverConfig) Validate() error {
	switch {
	// check that ports are greater than 0
	case s.HTTPConfig.Port < 1:
		return fmt.Errorf("http port %d is invalid", s.HTTPConfig.Port)
	case s.InfluxConfig.Port < 1:
		return fmt.Errorf("influx port %d is invalid", s.InfluxConfig.Port)
	// only check the edgex port if we need to register the server as a REST client
	case s.EdgeXConfig.RegisterRESTClient && s.EdgeXConfig.ExportDistroPort < 1:
		return fmt.Errorf("edgex export-distro port %d is invalid", s.EdgeXConfig.ExportDistroPort)
	// check the database name
	case s.InfluxConfig.DBName == "":
		return fmt.Errorf("influx dbname %s is invalid", s.InfluxConfig.DBName)
	// check the database precision
	case !validDBPrecision(s.InfluxConfig.DBPrecision):
		return fmt.Errorf("influx db precision %s is invalid", s.InfluxConfig.DBPrecision)
	default:
		return nil
	}
}

// SetDefault sets default values for the config
func (s *serverConfig) SetDefault() error {
	s.HTTPConfig.Port = 8080
	s.InfluxConfig.Host = "localhost"
	s.InfluxConfig.Port = 8086
	s.InfluxConfig.DBName = "edgex"
	s.InfluxConfig.DBPrecision = "ns"
	s.EdgeXConfig.RegisterRESTClient = false
	s.EdgeXConfig.CleanRegistration = true
	s.EdgeXConfig.ExportDistroHost = "localhost"
	s.EdgeXConfig.ExportDistroPort = 48071
	return nil
}

// MarshalTOML marshals the config into bytes
func (s *serverConfig) MarshalTOML() ([]byte, error) {
	return toml.Marshal(*s)
}

// UnmarshalTOML unmarshals the toml bytes into the config
func (s *serverConfig) UnmarshalTOML(bytes []byte) error {
	return toml.Unmarshal(bytes, s)
}

func init() {
	// initialize a default config here globally because then it simplifies
	// the various command Execute() methods
	Config = &serverConfig{}
	Config.SetDefault()
}

// Command is the command for application management
type Command struct {
	Start      StartCmd  `command:"start" description:"Start the server"`
	Config     ConfigCmd `command:"config" description:"Change or get config values"`
	ConfigFile string    `short:"c" long:"config-file" description:"Configuration file to use" required:"yes"`
}

// The current input command
var currentCmd Command

// ConfigCmd is for a set of commands working with the config file programmatically
type ConfigCmd struct {
	Check      CheckConfigCmd  `command:"check" descripttion:"Check a configuration file"`
	SnapUpdate UpdateConfigCmd `command:"update" description:"Update the configuration"`
	Set        SetConfigCmd    `command:"set" description:"Set values in the configuration file"`
	Get        GetConfigCmd    `command:"get" description:"Get values from the configuration file"`
}

// UpdateConfigCmd is a command for updating a config file from snapd/snapctl environment values
type UpdateConfigCmd struct{}

// Execute of UpdateConfigCmd will update a config file using values from snapd / snapctl
func (cmd *UpdateConfigCmd) Execute(args []string) error {
	err := tc.LoadTomlConfigurator(currentCmd.ConfigFile, Config)
	if err != nil {
		return err
	}

	// Get all keys of the toml
	keys, err := tc.TomlKeys(Config)
	if err != nil {
		return err
	}

	// Get all the values of these keys from snapd
	snapValues, err := getSnapKeyValues(keys)
	if err != nil {
		return err
	}

	// Write the values into the config
	err = tc.SetTomlConfiguratorKeyValues(snapValues, Config)
	if err != nil {
		return err
	}

	// Finally write out the config to the config file file
	return tc.WriteTomlConfigurator(currentCmd.ConfigFile, Config)
}

// getSnapKeyValues queries snapctl for all key values at once as JSON, and returns the corresponding values
func getSnapKeyValues(keys []string) (map[string]interface{}, error) {
	// get all values from snap at once as a json document
	snapCmd := exec.Command("snapctl", append([]string{"get", "-d"}, keys...)...)
	out, err := snapCmd.Output()
	if err != nil {
		return nil, err
	}

	// Unmarshal the json into the map, and return it
	returnMap := make(map[string]interface{})
	err = json.Unmarshal(out, &returnMap)
	if err != nil {
		return nil, err
	}

	return returnMap, nil
}

// SetConfigCmd is a command for setting config values in the config file
type SetConfigCmd struct {
	Args struct {
		Key   string `positional-arg-name:"key"`
		Value string `positional-arg-name:"value"`
	} `positional-args:"yes" required:"yes"`
}

// Execute of SetConfigCmd will set config values from the command line inside the config file
// TODO: not implemented yet
func (cmd *SetConfigCmd) Execute(args []string) error {
	// assume the value is a single valid json value to parse it
	var val interface{}
	err := json.Unmarshal([]byte(cmd.Args.Value), &val)
	if err != nil {
		return err
	}

	// load the toml configuration so we can manipulate it
	err = tc.LoadTomlConfigurator(currentCmd.ConfigFile, Config)
	if err != nil {
		return err
	}

	// try to set the value into the toml file using the key
	err = tc.SetTomlConfiguratorKeyVal(Config, cmd.Args.Key, val)
	if err != nil {
		return err
	}

	// finally write the configuration back out to the file
	return tc.WriteTomlConfigurator(currentCmd.ConfigFile, Config)
}

// GetConfigCmd is a command for getting config values from the config file
type GetConfigCmd struct {
	Args struct {
		Key string `positional-arg-name:"key"`
	} `positional-args:"yes" required:"yes"`
}

// Execute of GetConfigCmd will print off config values from the command line as specified in the config file
// TODO: not implemented yet
func (cmd *GetConfigCmd) Execute(args []string) (err error) {
	// load the toml configuration so we can manipulate it
	err = tc.LoadTomlConfigurator(currentCmd.ConfigFile, Config)
	if err != nil {
		return err
	}

	// try to set the value into the toml file using the key
	val, err := tc.GetTomlConfiguratorKeyVal(Config, cmd.Args.Key)
	if err != nil {
		return err
	}

	// Get the key from the tree
	fmt.Println(val)
	return
}

// CheckConfigCmd is a command for verifying a config file is valid and optionally
// creating a new one if the specified config file doesn't exist
type CheckConfigCmd struct {
	WriteNewFile bool `short:"w" long:"write-new" description:"Whether to write a new config if the specified file doesn't exist"`
}

// Execute of CheckConfigCmd checks if the specified config file exists, and if it doesn't it creates one
//
func (cmd *CheckConfigCmd) Execute(args []string) (err error) {
	// first check if the specified file exists
	if _, err = os.Stat(currentCmd.ConfigFile); os.IsNotExist(err) {
		// file doesn't exist
		if cmd.WriteNewFile {
			// write out a new file
			return tc.WriteTomlConfigurator(currentCmd.ConfigFile, Config)
		}
		return fmt.Errorf(
			"config file %s doesn't exist",
			currentCmd.ConfigFile,
		)
	}
	// otherwise the file exists, so load it to check it
	return tc.LoadTomlConfigurator(currentCmd.ConfigFile, Config)
}

// StartCmd command for creating an application
type StartCmd struct{}

// Execute of StartCmd will start running the web server
func (cmd *StartCmd) Execute(args []string) (err error) {
	// load the configuration for the server
	err = tc.LoadTomlConfigurator(currentCmd.ConfigFile, Config)
	if err != nil {
		return err
	}

	edgexConfig := Config.EdgeXConfig
	httpConfig := Config.HTTPConfig
	influxConfig := Config.InfluxConfig

	// Before starting the server, check if we need to register this REST server as a client with Edgex Distro
	if edgexConfig.RegisterRESTClient {
		edgexRegistrationEndpoint := fmt.Sprintf("http://%s:%d/api/v1/registration", edgexConfig.ExportDistroHost, edgexConfig.ExportDistroPort)
		// Since we're creating a new REST registration, check if we should delete the current registration first
		if edgexConfig.CleanRegistration {
			// Make a new client + DELETE request against the registration endpoint with the registration name directory
			client := &http.Client{}
			req, err := http.NewRequest("DELETE", edgexRegistrationEndpoint+"/name/golang-server", nil)
			if err != nil {
				return err
			}
			_, err = client.Do(req)
			if err != nil {
				return err
			}
		}

		// Because the export-distro service needs to make a network connection to this machine, and may be on a different machine than this one
		// we need to find the ip address that edgex should use when connecting to this machine dynamically, as the hostname specified on the command line
		// may be something like localhost, etc.
		// To handle this, we make a connection to the edgex distro host specified, and then look at the local address used by that socket connection
		// this also handles the case where there's some kind of proxy, but obviously that proxy needs to be setup to forward data from edgex to this server
		// but that's a whole different problem
		conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", edgexConfig.ExportDistroHost, edgexConfig.ExportDistroPort))
		if err != nil {
			return err
		}

		// Cast the address to a UDP address to get the IP as a net.IP
		localAddr, ok := conn.LocalAddr().(*net.UDPAddr)

		// don't defer closing the connection because if successful, we never actually exit this function when we start listening for new HTTP connections
		conn.Close()

		if !ok {
			return fmt.Errorf("invalid address object: %+v", conn.LocalAddr())
		}

		// in some instances the localhost ip may be returned as ipv6 ::1, so we should turn all loopback addresses to just localhost for compatibility
		var thisAddr string
		if localAddr.IP.IsLoopback() {
			thisAddr = "localhost"
		} else {
			thisAddr = localAddr.IP.String()
		}

		// Format the registration json with the IP address we just found for this server and the specified port we bind on
		registerJSON := fmt.Sprintf(edgeXCreateRESTRegistrationJSON, thisAddr, httpConfig.Port)

		// POST the request to export-client's registation endpoint with the formatted JSON as the body
		res, err := http.Post(
			edgexRegistrationEndpoint,
			"application/json",
			bytes.NewBufferString(registerJSON),
		)
		if err != nil {
			return err
		}

		if res.StatusCode != 201 {
			body, err := ioutil.ReadAll(res.Body)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to register REST endpoint with export-distro (status code : %d): %s", res.StatusCode, body)
		}
	}

	// Make a new HTTP client connection to influxdb
	influxClient, err := influx.NewHTTPClient(influx.HTTPConfig{
		Addr: fmt.Sprintf("http://%s:%d", influxConfig.Host, influxConfig.Port),
	})

	if err != nil {
		return err
	}

	// we only close the client once the function returns, as we don't return from this function unless error, but we will keep
	// using the influx client until an error happens
	defer influxClient.Close()

	ptConfig := influx.BatchPointsConfig{
		Database:  influxConfig.DBName,
		Precision: influxConfig.DBPrecision,
	}

	// start the HTTP server passing the influxClient in as a parameter for all
	http.HandleFunc("/edgex", timeInfluxData(influxClient, ptConfig))
	return http.ListenAndServe(fmt.Sprintf("%s:%d", httpConfig.Host, httpConfig.Port), nil)
}

func timeInfluxData(influxClient influx.Client, ptConfig influx.BatchPointsConfig) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// time the function and output how long it took as well as the method
		start := time.Now()
		readingData(w, r, influxClient, ptConfig)
		log.Printf("%s %s %d us\n", r.URL.Path, r.Method, time.Since(start)/time.Microsecond)
	}
}

func readingData(w http.ResponseWriter, req *http.Request, influxClient influx.Client, ptConfig influx.BatchPointsConfig) {
	// make sure that the method is a post
	if strings.ToUpper(req.Method) != "POST" {
		// error only post is supported, add Allow header with just POST
		// and add MethodNotAllowed response code
		w.Header().Add("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, err := w.Write([]byte("invalid request, only POST supported"))
		if err != nil {
			log.Printf("error writing error response")
		}
		return
	}

	// attempt to decode the body as an event JSON
	var event models.Event
	err := json.NewDecoder(req.Body).Decode(&event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// now that we have the event, in another go routine
	// we send the event's readings to influxdb
	// this is because we don't need to block this http request with the result of sending
	// to influx
	go sendEventToInflux(influxClient, ptConfig, event)
	w.WriteHeader(http.StatusOK)
}

func sendEventToInflux(influxClient influx.Client, ptConfig influx.BatchPointsConfig, event models.Event) {
	// Make a new set of batch points for this event
	bp, _ := influx.NewBatchPoints(ptConfig)

	// Add all readings into batch influxdb points
	for _, reading := range event.Readings {

		// Make the map of metadata for this reading (i.e. "tags" in influxdb parlance)
		tags := map[string]string{
			"id": reading.Id,
		}

		// Make a map for the reading values (i.e "fields" in influxdb parlance) and
		// dynamically attempt to parse the type of value for this reading - edgex returns
		// all reading types as strings, so we have to do a little bit of guesswork to
		// figure out the actual underlying value type
		// For the field name, we use the reading name here
		fields := make(map[string]interface{})
		readingType, boolVal, floatVal, intVal := parseValueType(reading.Value)
		switch readingType {
		case boolType:
			fields[reading.Name] = boolVal
		case intType:
			fields[reading.Name] = intVal
		case floatType:
			fields[reading.Name] = floatVal
		case stringType:
			fields[reading.Name] = reading.Value
		}

		// Calculate the unix time from the origin time in the reading
		// note that the origin time is in milliseconds
		unixTime := float64(reading.Origin) / float64(time.Second/time.Millisecond)
		unixTimeSec := math.Floor(unixTime)
		unixTimeNSec := int64((unixTime - unixTimeSec) * float64(time.Second/time.Nanosecond))

		// Make the reading point for this device
		pt, err := influx.NewPoint(
			reading.Device,
			tags,
			fields,
			// need to make sure the Time value returned is in UTC - but note we don't have to convert it before hand
			// because Unix time is always in UTC, but time.Time is in the local timezone
			time.Unix(int64(unixTimeSec), unixTimeNSec),
		)
		if err != nil {
			// TODO : send error via channel
			log.Printf("error creating reading point: %+v\n", err)
		}

		// Add it to the batch set
		bp.AddPoint(pt)
	}

	// finally write all these points out to influx
	err := influxClient.Write(bp)
	if err != nil {
		log.Printf("error writing points to influx: %+v\n", err)
	}
}

// parseValueType attempts to parse the value of the string value into a proper go type
func parseValueType(valueStr string) (typeStr dataValueType, boolVal bool, floatVal float64, intVal int64) {
	// first check for boolean
	fixedStr := strings.TrimSpace(strings.ToLower(valueStr))
	if fixedStr == "true" {
		typeStr = boolType
		boolVal = true
		return
	} else if fixedStr == "false" {
		typeStr = boolType
		boolVal = false
		return
	}

	// check for integer
	intVal, err := strconv.ParseInt(fixedStr, 10, 64)
	if err == nil {
		// then it's an int value
		typeStr = intType
		return
	}

	// finally check for a floating point value
	floatVal, err = strconv.ParseFloat(fixedStr, 64)
	if err == nil {
		// success, value is a float
		typeStr = floatType
		return
	}

	// if we get here, it's not any scalar numeric value, so just assume it's meant as a string
	typeStr = stringType
	return
}

// command parser
var parser = flags.NewParser(&currentCmd, flags.Default)

// empty - the command execution happens in *.Execute methods
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
}
