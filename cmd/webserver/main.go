package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	MQTT "github.com/eclipse/paho.mqtt.golang"
	"github.com/edgexfoundry/edgex-go/core/domain/models"
	flags "github.com/jessevdk/go-flags"
	"github.com/pkg/bson"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/plotutil"
	"gonum.org/v1/plot/vg"
	yaml "gopkg.in/yaml.v2"
)

const (
	DEBUG            = true
	HTTPUnknownError = iota
	HTTPInvalidFormat
	HTTPInvalidName
	HTTPInvalidNumber
	HTTPInvalidReading
	HTTPPlotFailure
)

// Command is the command for application management
type Command struct {
	Start StartCmd `command:"start" description:"Start the server"`
}

// HTTPErrorResponse is for errors to be returned via HTTP
type HTTPErrorResponse struct {
	Error string `json:"error_msg" yaml:"error_msg"`
	Code  int    `json:"error_code" yaml:"error_code"`
}

// The current input command
var cmd Command

// StartCmd command for creating an application
type StartCmd struct {
	// general opts
	Verbose bool `short:"v" long:"verbose" description:"Verbose output"`

	// mqtt opts
	MQTTPort       uint   `short:"m" long:"mqtt-port" description:"MQTT server port to connect to" default:"1883"`
	MQTTSSL        bool   `short:"e" long:"mqtt-ssl" description:"MQTT connection protocol (default no encryption)"`
	MQTTHost       string `short:"b" long:"mqtt-host" description:"MQTT server hostname to connect to" default:"localhost"`
	MQTTClientName string `short:"c" long:"mqtt-client" description:"MQTT clientname to use (default is automatically generated)"`
	MQTTTopic      string `short:"t" long:"mqtt-topic" description:"MQTT topic name to subscribe on" default:"EdgeXDataTopic"`
	MQTTUsername   string `short:"u" long:"mqtt-user" description:"MQTT server username"`
	MQTTPassword   string `short:"p" long:"mqtt-passwd" description:"MQTT server password"`
	MQTTQoS        int    `short:"q" long:"mqtt-qos" choice:"0" choice:"1" choice:"2" description:"MQTT Quality Of Service for the topic"`
	MQTTSCertAuth  string `short:"i" long:"mqtt-cert" description:"MQTT secure certificate file"`

	// http opts
	HTTPPort uint   `short:"s" long:"http-port" description:"HTTP server port to bind on" default:"8080"`
	HTTPHost string `short:"a" long:"http-host" description:"HTTP server hostname to bind on" default:"0.0.0.0"`
}

// StartCmd will start running the web server
func (cmd *StartCmd) Execute(args []string) (err error) {
	// Make an options struct for the mqtt client
	connOpts := MQTT.NewClientOptions()

	// always use a clean session
	connOpts.SetCleanSession(true)

	// always reconnect
	connOpts.SetAutoReconnect(true)

	// set the connection and connection lost handler
	connOpts.SetConnectionLostHandler(onDisconnect)
	connOpts.SetOnConnectHandler(onConnect)

	// Setup the broker hostname, checking for SSL
	var brokerHost string
	if cmd.MQTTSSL {
		// need to include SSL in the URL
		brokerHost = fmt.Sprintf("ssl://%s:%d", cmd.MQTTHost, cmd.MQTTPort)

		conf := &tls.Config{
			InsecureSkipVerify: false,
		}

		// if an additional certificate authority is necessary,
		// load that ca certificate
		if cmd.MQTTSCertAuth != "" {
			cert, err := ioutil.ReadFile(cmd.MQTTSCertAuth)
			if err != nil {
				return err
			}

			certPool := x509.NewCertPool()
			certPool.AppendCertsFromPEM(cert)

			conf.RootCAs = certPool
		}
		connOpts.SetTLSConfig(conf)
	} else {
		brokerHost = fmt.Sprintf("tcp://%s:%d", cmd.MQTTHost, cmd.MQTTPort)
	}
	connOpts.AddBroker(brokerHost)

	// Set the client ID
	var clientID string
	if cmd.MQTTClientName == "" {
		// Generate a new client name
		clientID = genNewClientID()
	} else {
		// Use what was specified in the options
		clientID = cmd.MQTTClientName
	}
	connOpts.SetClientID(clientID)

	// Setup username/password
	connOpts.SetUsername(cmd.MQTTUsername)
	connOpts.SetPassword(cmd.MQTTPassword)

	// Attempt to make the connection
	client := MQTT.NewClient(connOpts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return token.Error()
	} else {
		// Now subscribe to the topic
		if token := client.Subscribe(cmd.MQTTTopic, byte(cmd.MQTTQoS), onMessageReceived); token.Wait() && token.Error() != nil {
			return token.Error()
		}
		fmt.Printf("Connected to %s\n", brokerHost)
	}

	// Start up the web server
	http.HandleFunc("/data", currentData)
	http.HandleFunc("/plot", plotData)
	err = http.ListenAndServe(fmt.Sprintf("%s:%d", cmd.HTTPHost, cmd.HTTPPort), nil)

	// Gracefully disconnect the client and return an error
	client.Disconnect(0)

	return err
}

func genNewClientID() string {
	// TODO: generate random
	return "unique"
}

var dataStore map[string][]models.Reading

// TODO: this should parse the message from edgex and save it into an in memory database, etc.
func onMessageReceived(client MQTT.Client, message MQTT.Message) {
	var event models.Event
	err := json.Unmarshal(message.Payload(), &event)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error decoding message: %+v", err)
	} else {
		// fmt.Printf("event : %+v\n", event)

		// Store all of the readings into the in memory data store
		for _, reading := range event.Readings {
			if _, ok := dataStore[reading.Name]; !ok {
				dataStore[reading.Name] = make([]models.Reading, 0)
			}
			dataStore[reading.Name] = append(dataStore[reading.Name], reading)
		}
	}
}

// mqtt disconnect callback
func onDisconnect(client MQTT.Client, err error) {
	fmt.Printf("client disconnected: %+v\n", err)
}

// mqtt connect callback
func onConnect(client MQTT.Client) {
	fmt.Println("client connected")
}

// formatResponse will take the provided value and output it to the http response
// in the specified format
// It handles any encoding errors and returns
// Current supported formats are :
// - YAML
// - JSON
// - BSON
// - GOB
// - XML (note this doesn't work for all data types - notably maps don't serialize with XML)
func formatResponse(format string, w http.ResponseWriter, val interface{}) {
	// to force browser to display the page, useful in debugging
	if DEBUG {
		w.Header().Set("Content-Disposition", "inline")
	}
	errCode := HTTPUnknownError
	status := http.StatusInternalServerError
	var err error
	switch strings.ToLower(format) {
	case "":
		// default to json if not specified
		fallthrough
	case "json":
		err = json.NewEncoder(w).Encode(val)
		// only set the content-type if we're successful
		// if there was some error, we'll let the error handler set the content-type
		if err == nil {
			w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		}
	case "bson":
		err = bson.NewEncoder(w).Encode(val)
		// only set the content-type if we're successful
		// if there was some error, we'll let the error handler set the content-type
		if err == nil {
			w.Header().Set("Content-Type", "application/bson; charset=UTF-8")
		}
	case "yaml":
		err = yaml.NewEncoder(w).Encode(val)
		if err == nil {
			w.Header().Set("Content-Type", "text/x-yaml; charset=UTF-8")
		}
	case "gob":
		err = gob.NewEncoder(w).Encode(val)
		if err == nil {
			w.Header().Set("Content-Type", "application/x-gob; charset=UTF-8")
		}
	case "xml":
		err = xml.NewEncoder(w).Encode(val)
		if err == nil {
			w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
		}
	default:
		err = fmt.Errorf("invalid format: %v", format)
		errCode = HTTPInvalidFormat
		status = http.StatusBadRequest
	}

	if err != nil {
		// some sort of error writing out the response
		sendError(w, status, err, errCode)
		log.Printf("error: failed to encode response: %+v\n", err)
	}
}

// sendError will send the error details in JSON to the http response specified
func sendError(w http.ResponseWriter, status int, err error, code int) {
	w.WriteHeader(status)
	// we always return error codes in json
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if err := json.NewEncoder(w).Encode(HTTPErrorResponse{
		Error: err.Error(),
		Code:  code,
	}); err != nil {
		// nothing more we can return to the client
		log.Printf("error: failed to encode error response: %+v\n", err)
	}
}

// handler for getting all current readings
func currentData(w http.ResponseWriter, req *http.Request) {
	// get format rest parameter
	format := req.URL.Query().Get("format")
	formatResponse(format, w, dataStore)
}

// handler for generating a plot of data
func plotData(w http.ResponseWriter, req *http.Request) {
	// get the name of the data stream to plot
	queryParams := req.URL.Query()
	name := queryParams.Get("name")

	// ensure that the specified sensor name was specified, actually exists, and has data to plot
	var ok bool
	var readings []models.Reading
	if readings, ok = dataStore[name]; name == "" || !ok || readings == nil || len(readings) == 0 {
		sendError(w, http.StatusBadRequest, errors.New("invalid data source name"), HTTPInvalidName)
		return
	}

	// get the number of readings to use
	numStr := queryParams.Get("num")
	var numToKeep uint64
	numToKeep = math.MaxUint64
	if numStr != "" {
		// get the number of readings
		var err error
		if numToKeep, err = strconv.ParseUint(numStr, 10, 64); err != nil || numToKeep == 0 {
			sendError(w, http.StatusBadRequest, err, HTTPInvalidNumber)
			return
		}
	}

	// we store the rest axis limit settings inside a map, so that to check if the value was set or not
	// we can simply check to see if the key is inside the map
	var axislimits = map[string]float64{}
	// REST parameter names
	axislimitNames := []string{"xmin", "xmax", "ymin", "ymax"}
	for _, axisLimit := range axislimitNames {
		// get the value from the queries
		limStr := queryParams.Get(axisLimit)
		if limStr != "" {
			// it was specified, so parse it and save it
			lim, err := strconv.ParseFloat(limStr, 64)
			if err != nil {
				sendError(w, http.StatusBadRequest, err, HTTPInvalidNumber)
				return
			}

			// parsed successfully, save it
			axislimits[axisLimit] = lim
		}
	}

	// Calculate the size of the returned array and the index to start with in the readings array
	size := minUint64(numToKeep, uint64(len(readings)))
	rStart := maxUint64(1, uint64(len(readings))-size)

	// allocate the time and data values as a x,y list
	pts := make(plotter.XYs, size)

	// for keeping track of the min / max values - we use this to set the axis limits nicely
	maxY := 0.0
	minY := 0.0

	var err error
	// now go through the events and save them into separate x,y lists
	for index, rIndex := uint64(0), rStart-1; index < size; index, rIndex = index+1, rIndex+1 {
		// Save the time value as a float64, and parse the value string into a float64 as well
		pts[index].X = float64(readings[rIndex].Origin) / 1000.0
		pts[index].Y, err = strconv.ParseFloat(readings[rIndex].Value, 64)
		if err != nil {
			sendError(w, http.StatusInternalServerError, err, HTTPInvalidReading)
			return
		}

		if pts[index].Y > maxY {
			maxY = pts[index].Y
		}

		if pts[index].Y < minY {
			minY = pts[index].Y
		}
	}

	// now make a new plot object to write out the data to
	p, err := plot.New()
	if err != nil {
		sendError(w, http.StatusInternalServerError, err, HTTPPlotFailure)
		return
	}

	p.Title.Text = fmt.Sprintf("%s %s Data", readings[0].Device, name)
	p.X.Tick.Marker = plot.TimeTicks{Format: "15:04:05.000"}
	p.X.Label.Text = "Time"
	p.Y.Label.Text = name

	// set the y axis so that there's some wiggle room above and below the plot
	// if the value was set via REST however, then just use that value
	if lim, ok := axislimits["ymin"]; ok {
		p.Y.Min = lim
	} else {
		if minY > 0 {
			p.Y.Min = 0.0
		} else {
			p.Y.Min = minY * 1.25
		}
	}
	if lim, ok := axislimits["ymax"]; ok {
		p.Y.Max = lim
	} else {
		if maxY < 0 {
			p.Y.Max = 0.0
		} else {
			p.Y.Max = maxY * 1.25
		}
	}

	// for x axis limits, if it wasn't configured via REST, just let the plotting library figure it out
	if lim, ok := axislimits["xmax"]; ok {
		p.X.Max = lim
	}
	if lim, ok := axislimits["xmin"]; ok {
		p.X.Min = lim
	}

	// add the points to the plot with the name of the sensor
	err = plotutil.AddLinePoints(p, name, pts)
	if err != nil {
		sendError(w, http.StatusInternalServerError, err, HTTPPlotFailure)
		return
	}

	//make a WriterTo that we can use to write the image out to the screen with
	height := 6 * vg.Inch
	// golden ratio
	width := 1.61803398875 * height
	plotWriter, err := p.WriterTo(width, height, "svg")
	if err != nil {
		sendError(w, http.StatusInternalServerError, err, HTTPPlotFailure)
		return
	}

	// need to set the content-type for this as well
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Disposition", "inline")
	plotWriter.WriteTo(w)
}

// command parser
var parser = flags.NewParser(&cmd, flags.Default)

// empty - the command execution happens in *.Execute methods
func main() {
	dataStore = make(map[string][]models.Reading)
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
}

func minUint64(x, y uint64) uint64 {
	if x < y {
		return x
	}
	return y
}

func maxUint64(x, y uint64) uint64 {
	if x > y {
		return x
	}
	return y
}
