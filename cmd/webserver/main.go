package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"syscall"

	MQTT "github.com/eclipse/paho.mqtt.golang"
	flags "github.com/jessevdk/go-flags"
)

// Command is the command for application management
type Command struct {
	Start StartCmd `command:"start" description:"Start the server"`
}

// The current input command
var cmd Command

// StartCmd command for creating an application
type StartCmd struct {
	// general opts
	Verbose bool `short:"v" long:"verbose" description:"Verbose output"`

	// mqtt opts
	MQTTPort       int    `short:"m" long:"mqtt-port" description:"MQTT server port to connect to" default:"1883"`
	MQTTSSL        bool   `short:"e" long:"mqtt-ssl" description:"MQTT connection protocol (default no encryption)"`
	MQTTHost       string `short:"b" long:"mqtt-host" description:"MQTT server hostname to connect to" default:"localhost"`
	MQTTClientName string `short:"c" long:"mqtt-client" description:"MQTT clientname to use (default is automatically generated)"`
	MQTTTopic      string `short:"t" long:"mqtt-topic" description:"MQTT topic name to subscribe on" default:"EdgeXDataTopic"`
	MQTTUsername   string `short:"u" long:"mqtt-user" description:"MQTT server username"`
	MQTTPassword   string `short:"p" long:"mqtt-passwd" description:"MQTT server password"`
	MQTTQoS        int    `short:"q" long:"mqtt-qos" choice:"0" choice:"1" choice:"2" description:"MQTT Quality Of Service for the topic"`
	MQTTSCertAuth  string `short:"i" long:"mqtt-cert" description:"MQTT secure certificate file"`

	// http opts
	HTTPPort int    `short:"s" long:"http-port" description:"HTTP server port to bind on" default:"8080"`
	HTTPHost string `short:"a" long:"http-host" description:"HTTP server hostname to bind on" default:"0.0.0.0"`
}

// StartCmd will start running the web server
func (cmd *StartCmd) Execute(args []string) (err error) {
	// For safely dying
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// Make an options struct for the mqtt client
	connOpts := MQTT.NewClientOptions()

	// always use a clean session
	connOpts.SetCleanSession(true)

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

	// Wait for signals and then disconnect safely
	<-c
	client.Disconnect(0)

	return err
}

func genNewClientID() string {
	// TODO: generate random
	return "unique"
}

// TODO: this should parse the message from edgex and save it into an in memory database, etc.
func onMessageReceived(client MQTT.Client, message MQTT.Message) {
	fmt.Printf("Received message on topic: %s\nMessage: %s\n", message.Topic(), message.Payload())
}

// command parser
var parser = flags.NewParser(&cmd, flags.Default)

// empty - the command execution happens in *.Execute methods
func main() {
	_, err := parser.Parse()
	if err != nil {
		os.Exit(1)
	}
}
