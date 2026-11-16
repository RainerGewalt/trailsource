// Package mqttpub publishes records and a retained health topic over MQTT.
// The health topic carries an MQTT Last Will so a broker-side disconnect is
// visible as a retained "down" state. Retain is used only for state, never
// for records.
package mqttpub

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Options configures the publisher.
type Options struct {
	Broker   string
	ClientID string
	Username string
	Password string
	QoS      byte

	HealthTopic string // retained; also the will topic
	WillPayload []byte // published by the broker when the session dies

	UseTLS             bool
	CACertPath         string
	ClientCertPath     string
	ClientKeyPath      string
	InsecureSkipVerify bool
	ServerName         string

	OnConnect func()
}

// New connects to the broker. It blocks until the first connection succeeded
// or the timeout elapsed.
func New(options Options) (*Publisher, error) {
	co := mqtt.NewClientOptions().
		AddBroker(options.Broker).
		SetClientID(options.ClientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(15 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPingTimeout(10 * time.Second).
		SetConnectTimeout(20 * time.Second).
		SetWriteTimeout(20 * time.Second).
		SetOrderMatters(true).
		SetProtocolVersion(4) // MQTT 3.1.1

	if options.Username != "" {
		co.SetUsername(options.Username)
		co.SetPassword(options.Password)
	}
	if options.HealthTopic != "" && len(options.WillPayload) > 0 {
		co.SetBinaryWill(options.HealthTopic, options.WillPayload, options.QoS, true)
	}
	if options.UseTLS || strings.HasPrefix(options.Broker, "ssl://") || strings.HasPrefix(options.Broker, "tls://") || strings.HasPrefix(options.Broker, "mqtts://") {
		tlsConfig, err := createTLSConfig(options)
		if err != nil {
			return nil, err
		}
		co.SetTLSConfig(tlsConfig)
	}
	co.SetOnConnectHandler(func(_ mqtt.Client) {
		log.Printf("mqtt connected broker=%s client_id=%s", options.Broker, options.ClientID)
		if options.OnConnect != nil {
			options.OnConnect()
		}
	})
	co.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("mqtt connection lost: %v", err)
	})

	client := mqtt.NewClient(co)
	token := client.Connect()
	if !token.WaitTimeout(25 * time.Second) {
		// The adapter starts only with a broker: no record is sealed before
		// this connection exists (docs/ARCHITECTURE.md, "Broker outage").
		client.Disconnect(0)
		return nil, fmt.Errorf("mqtt connect timeout: no connection to %s within 25 s; start the broker or correct mqtt.broker", options.Broker)
	}
	if err := token.Error(); err != nil {
		client.Disconnect(0)
		return nil, fmt.Errorf("mqtt connect: %w", err)
	}
	return newPublisher(client, options.QoS, options.HealthTopic, 256, 15*time.Second), nil
}

func createTLSConfig(options Options) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: options.InsecureSkipVerify, //nolint:gosec // explicit operator choice for lab setups
	}
	if strings.TrimSpace(options.ServerName) != "" {
		tlsConfig.ServerName = strings.TrimSpace(options.ServerName)
	} else if !options.InsecureSkipVerify {
		if host := hostFromBrokerURL(options.Broker); host != "" && net.ParseIP(host) == nil {
			tlsConfig.ServerName = host
		}
	}
	if options.CACertPath != "" {
		caCert, err := os.ReadFile(options.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("read MQTT CA certificate %q: %w", options.CACertPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("parse MQTT CA certificate %q", options.CACertPath)
		}
		tlsConfig.RootCAs = pool
	}
	if options.ClientCertPath != "" || options.ClientKeyPath != "" {
		if options.ClientCertPath == "" || options.ClientKeyPath == "" {
			return nil, fmt.Errorf("mqtt.tls.client_cert and mqtt.tls.client_key are both required for mTLS")
		}
		cert, err := tls.LoadX509KeyPair(options.ClientCertPath, options.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load MQTT client certificate/key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	return tlsConfig, nil
}

func hostFromBrokerURL(broker string) string {
	u, err := url.Parse(broker)
	if err != nil {
		return ""
	}
	if h := u.Hostname(); h != "" {
		return h
	}
	h, _, err := net.SplitHostPort(broker)
	if err == nil {
		return h
	}
	return broker
}
