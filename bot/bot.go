// © 2013 the CatBase Authors under the WTFPL. See AUTHORS for the list of authors.

package bot

import (
	"html/template"
	"log"
	"net/http"
	"reflect"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/velour/catbase/bot/msg"
	"github.com/velour/catbase/bot/msglog"
	"github.com/velour/catbase/bot/user"
	"github.com/velour/catbase/config"
)

// bot type provides storage for bot-wide information, configs, and database connections
type bot struct {
	// Each plugin must be registered in our plugins handler. To come: a map so that this
	// will allow plugins to respond to specific kinds of events
	plugins        map[string]Plugin
	pluginOrdering []string

	// Users holds information about all of our friends
	users []user.User
	// Represents the bot
	me user.User

	config *config.Config

	conn Connector

	logIn  chan msg.Message
	logOut chan msg.Messages

	version string

	// The entries to the bot's HTTP interface
	httpEndPoints map[string]string

	// filters registered by plugins
	filters map[string]func(string) string

	callbacks CallbackMap
}

// Variable represents a $var replacement
type Variable struct {
	Variable, Value string
}

// New creates a bot for a given connection and set of handlers.
func New(config *config.Config, connector Connector) Bot {
	logIn := make(chan msg.Message)
	logOut := make(chan msg.Messages)

	msglog.RunNew(logIn, logOut)

	users := []user.User{
		user.User{
			Name: config.Get("Nick", "bot"),
		},
	}

	bot := &bot{
		config:         config,
		plugins:        make(map[string]Plugin),
		pluginOrdering: make([]string, 0),
		conn:           connector,
		users:          users,
		me:             users[0],
		logIn:          logIn,
		logOut:         logOut,
		httpEndPoints:  make(map[string]string),
		filters:        make(map[string]func(string) string),
		callbacks:      make(CallbackMap),
	}

	bot.migrateDB()

	http.HandleFunc("/", bot.serveRoot)

	connector.RegisterEvent(bot.Receive)

	return bot
}

// Config gets the configuration that the bot is using
func (b *bot) Config() *config.Config {
	return b.config
}

func (b *bot) DB() *sqlx.DB {
	return b.config.DB
}

// Create any tables if necessary based on version of DB
// Plugins should create their own tables, these are only for official bot stuff
// Note: This does not return an error. Database issues are all fatal at this stage.
func (b *bot) migrateDB() {
	if _, err := b.DB().Exec(`create table if not exists variables (
			id integer primary key,
			name string,
			value string
		);`); err != nil {
		log.Fatal("Initial DB migration create variables table: ", err)
	}
}

// Adds a constructed handler to the bots handlers list
func (b *bot) AddPlugin(h Plugin) {
	name := reflect.TypeOf(h).String()
	b.plugins[name] = h
	b.pluginOrdering = append(b.pluginOrdering, name)
}

func (b *bot) Who(channel string) []user.User {
	names := b.conn.Who(channel)
	users := []user.User{}
	for _, n := range names {
		users = append(users, user.New(n))
	}
	return users
}

var rootIndex = `
<!DOCTYPE html>
<html>
	<head>
		<title>Factoids</title>
		<link rel="stylesheet" href="http://yui.yahooapis.com/pure/0.1.0/pure-min.css">
                <meta name="viewport" content="width=device-width, initial-scale=1">
	</head>
	{{if .EndPoints}}
	<div style="padding-top: 1em;">
		<table class="pure-table">
			<thead>
				<tr>
					<th>Plugin</th>
				</tr>
			</thead>

			<tbody>
				{{range $key, $value := .EndPoints}}
				<tr>
					<td><a href="{{$value}}">{{$key}}</a></td>
				</tr>
				{{end}}
			</tbody>
		</table>
	</div>
	{{end}}
</html>
`

func (b *bot) serveRoot(w http.ResponseWriter, r *http.Request) {
	context := make(map[string]interface{})
	context["EndPoints"] = b.httpEndPoints
	t, err := template.New("rootIndex").Parse(rootIndex)
	if err != nil {
		log.Println(err)
	}
	t.Execute(w, context)
}

// IsCmd checks if message is a command and returns its curtailed version
func IsCmd(c *config.Config, message string) (bool, string) {
	cmdcs := c.GetArray("CommandChar", []string{"!"})
	botnick := strings.ToLower(c.Get("Nick", "bot"))
	if botnick == "" {
		log.Fatalf(`You must run catbase -set nick -val <your bot nick>`)
	}
	iscmd := false
	lowerMessage := strings.ToLower(message)

	if strings.HasPrefix(lowerMessage, botnick) &&
		len(lowerMessage) > len(botnick) &&
		(lowerMessage[len(botnick)] == ',' || lowerMessage[len(botnick)] == ':') {

		iscmd = true
		message = message[len(botnick):]

		// trim off the customary addressing punctuation
		if message[0] == ':' || message[0] == ',' {
			message = message[1:]
		}
	} else {
		for _, cmdc := range cmdcs {
			if strings.HasPrefix(lowerMessage, cmdc) && len(cmdc) > 0 {
				iscmd = true
				message = message[len(cmdc):]
				break
			}
		}
	}

	// trim off any whitespace left on the message
	message = strings.TrimSpace(message)

	return iscmd, message
}

func (b *bot) CheckAdmin(nick string) bool {
	for _, u := range b.Config().GetArray("Admins", []string{}) {
		if nick == u {
			return true
		}
	}
	return false
}

var users = map[string]*user.User{}

func (b *bot) GetUser(nick string) *user.User {
	if _, ok := users[nick]; !ok {
		users[nick] = &user.User{
			Name:  nick,
			Admin: b.checkAdmin(nick),
		}
	}
	return users[nick]
}

func (b *bot) NewUser(nick string) *user.User {
	return &user.User{
		Name:  nick,
		Admin: b.checkAdmin(nick),
	}
}

func (b *bot) checkAdmin(nick string) bool {
	for _, u := range b.Config().GetArray("Admins", []string{}) {
		if nick == u {
			return true
		}
	}
	return false
}

// Register a text filter which every outgoing message is passed through
func (b *bot) RegisterFilter(name string, f func(string) string) {
	b.filters[name] = f
}

// Register a callback
func (b *bot) Register(p Plugin, kind Kind, cb Callback) {
	t := reflect.TypeOf(p)
	if _, ok := b.callbacks[t]; !ok {
		b.callbacks[t] = make(map[Kind][]Callback)
	}
	if _, ok := b.callbacks[t][kind]; !ok {
		b.callbacks[t][kind] = []Callback{}
	}
	b.callbacks[t][kind] = append(b.callbacks[t][kind], cb)
}

func (b *bot) RegisterWeb(root, name string) {
	b.httpEndPoints[name] = root
}
