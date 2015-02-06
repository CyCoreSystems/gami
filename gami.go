//GAMI
//Library for interacting with Asterisk AMI
package gami

import (
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"
	"syscall"
)

var ErrNotEvent error = errors.New("Not Event")
var ErrInvalidLogin error = errors.New("InvalidLogin AMI Interface")
var ErrNotAMI error = errors.New("Server not AMI interface")

type Params map[string]string

type AMIClient struct {
	conn     *textproto.Conn
	conn_raw *net.Conn

	address string
	amiUser string
	amiPass string

	//network wait for a new connection
	waitNewConnection chan struct{}

	response chan *AMIResponse

	//Events for client parse
	Events chan *AMIEvent

	//Raise error
	Error chan error

	//Network error
	NetError chan error
}

type AMIResponse struct {
	Status string
	Params map[string]string
}

//Representation of Event readed
type AMIEvent struct {
	//Identification of event Event: xxxx
	Id string

	Privilege []string

	//Rest of arguments received
	Params map[string]string
}

//authenticate to AMI
func (client *AMIClient) Login(username, password string) error {
	response, err := client.Action("Login", Params{"Username": username, "Secret": password})
	if err != nil {
		return err
	}

	if (*response).Status == "Error" {
		return errors.New((*response).Params["Message"])
	}

	client.amiUser = username
	client.amiPass = password
	return nil
}

//Reconnect the session, autologin
func (client *AMIClient) Reconnect() error {
	client.conn.Close()
	reconnect, err := Dial(client.address)
	if err != nil {
		return err
	}
	if err := reconnect.Login(client.amiUser, client.amiPass); err != nil {
		return err
	}

	client.conn = reconnect.conn
	client.conn_raw = reconnect.conn_raw
	client.waitNewConnection <- struct{}{}
	return nil
}

//Send a single action
func (client *AMIClient) Action(action string, params Params) (*AMIResponse, error) {

	if err := client.conn.PrintfLine("Action: %s", strings.TrimSpace(action)); err != nil {
		return nil, err
	}

	for k, v := range params {
		if err := client.conn.PrintfLine("%s: %s", k, strings.TrimSpace(v)); err != nil {
			return nil, err
		}
	}

	if err := client.conn.PrintfLine(""); err != nil {
		return nil, err
	}

	response := <-client.response
	return response, nil
}

//Process socket waiting events and responses
func (client *AMIClient) run() {

	go func() {

		for {

			data, err := client.conn.ReadMIMEHeader()

			if err != nil {
				switch err {
				case syscall.ECONNABORTED:
					fallthrough
				case syscall.ECONNRESET:
					fallthrough
				case syscall.ECONNREFUSED:
					fallthrough
				case io.EOF:
					client.NetError <- err
					<-client.waitNewConnection
				default:
					client.Error <- err
				}
				continue
			}

			if ev, err := newEvent(&data); err != nil {
				if err != ErrNotEvent {
					client.Error <- err
				}
			} else {
				client.Events <- ev
			}

			if response, err := newResponse(&data); err == nil {
				client.response <- response
			}

		}
	}()
}

func (client *AMIClient) Close() {
	client.Action("Logoff", nil)
	(*client.conn_raw).Close()
}

//get a response from action
func newResponse(data *textproto.MIMEHeader) (*AMIResponse, error) {
	if data.Get("Response") == "" {
		return nil, errors.New("Not Response")
	}
	response := &AMIResponse{"", make(map[string]string)}
	for k, v := range *data {
		if k == "Response" {
			continue
		}
		response.Params[k] = v[0]
	}
	response.Status = data.Get("Response")
	return response, nil
}

//build event
func newEvent(data *textproto.MIMEHeader) (*AMIEvent, error) {
	if data.Get("Event") == "" {
		return nil, ErrNotEvent
	}
	ev := &AMIEvent{data.Get("Event"), strings.Split(data.Get("Privilege"), ","), make(map[string]string)}
	for k, v := range *data {
		if k == "Event" || k == "Privilege" {
			continue
		}
		ev.Params[k] = v[0]
	}
	return ev, nil
}

//Create a new connection to AMI
func Dial(address string) (*AMIClient, error) {
	conn_raw, err := net.Dial("tcp", address)

	if err != nil {
		return nil, err
	}
	conn := textproto.NewConn(conn_raw)
	label, err := conn.ReadLine()
	if err != nil {
		return nil, err
	}

	if strings.Contains(label, "Asterisk Call Manager") != true {
		return nil, ErrNotAMI
	}

	client := &AMIClient{
		conn:              conn,
		conn_raw:          &conn_raw,
		address:           address,
		amiUser:           "",
		amiPass:           "",
		waitNewConnection: make(chan struct{}),
		response:          make(chan *AMIResponse),
		Events:            make(chan *AMIEvent, 100),
		Error:             make(chan error, 1),
		NetError:          make(chan error, 1),
	}
	client.run()
	return client, nil
}
