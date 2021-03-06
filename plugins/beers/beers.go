// © 2013 the CatBase Authors under the WTFPL. See AUTHORS for the list of authors.

package beers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/nfnt/resize"
	"github.com/rs/zerolog/log"

	"github.com/velour/catbase/bot"
	"github.com/velour/catbase/bot/msg"
	"github.com/velour/catbase/config"
	"github.com/velour/catbase/plugins/counter"
)

// This is a skeleton plugin to serve as an example and quick copy/paste for new plugins.

const itemName = ":beer:"

var cachedImages = map[string][]byte{}

type BeersPlugin struct {
	b  bot.Bot
	c  *config.Config
	db *sqlx.DB

	untapdCache map[int]bool
}

type untappdUser struct {
	id          int64
	untappdUser string
	channel     string
	lastCheckin int
	chanNick    string
}

// New BeersPlugin creates a new BeersPlugin with the Plugin interface
func New(b bot.Bot) *BeersPlugin {
	if _, err := b.DB().Exec(`create table if not exists untappd (
			id integer primary key,
			untappdUser string,
			channel string,
			lastCheckin integer,
			chanNick string
		);`); err != nil {
		log.Fatal().Err(err)
	}
	p := &BeersPlugin{
		b:  b,
		c:  b.Config(),
		db: b.DB(),

		untapdCache: make(map[int]bool),
	}

	b.Register(p, bot.Message, p.message)
	b.Register(p, bot.Help, p.help)

	p.registerWeb()

	token := p.c.Get("Untappd.Token", "NONE")
	if token == "NONE" || token == "" {
		log.Error().Msgf("No untappd token. Checking disabled.")
		return p
	}

	for _, channel := range p.c.GetArray("Untappd.Channels", []string{}) {
		go p.untappdLoop(b.DefaultConnector(), channel)
	}

	return p
}

// Message responds to the bot hook on recieving messages.
// This function returns true if the plugin responds in a meaningful way to the users message.
// Otherwise, the function returns false and the bot continues execution of other plugins.
func (p *BeersPlugin) message(c bot.Connector, kind bot.Kind, message msg.Message, args ...interface{}) bool {
	parts := strings.Fields(message.Body)

	if len(parts) == 0 {
		return false
	}

	channel := message.Channel
	user := message.User
	nick := user.Name

	// respond to the beers type of queries
	parts[0] = strings.ToLower(parts[0]) // support iPhone/Android saying "Beers"
	if parts[0] == "beers" {
		if len(parts) == 3 {

			// try to get a count out of parts[2]
			count, err := strconv.Atoi(parts[2])
			if err != nil {
				// if it's not a number, maybe it's a nick!
				p.b.Send(c, bot.Message, channel, "Sorry, that didn't make any sense.")
			}

			if count < 0 {
				// you can't be negative
				msg := fmt.Sprintf("Sorry %s, you can't have negative beers!", nick)
				p.b.Send(c, bot.Message, channel, msg)
				return true
			}
			if parts[1] == "+=" {
				p.addBeers(nick, count)
				p.randomReply(c, channel)
			} else if parts[1] == "=" {
				if count == 0 {
					p.puke(c, nick, channel)
				} else {
					p.setBeers(nick, count)
					p.randomReply(c, channel)
				}
			} else {
				p.b.Send(c, bot.Message, channel, "I don't know your math.")
			}
		} else if len(parts) == 2 {
			if p.doIKnow(parts[1]) {
				p.reportCount(c, parts[1], channel, false)
			} else {
				msg := fmt.Sprintf("Sorry, I don't know %s.", parts[1])
				p.b.Send(c, bot.Message, channel, msg)
			}
		} else if len(parts) == 1 {
			p.reportCount(c, nick, channel, true)
		}

		// no matter what, if we're in here, then we've responded
		return true
	} else if parts[0] == "puke" {
		p.puke(c, nick, channel)
		return true
	}

	if message.Command && parts[0] == "imbibe" {
		p.addBeers(nick, 1)
		p.randomReply(c, channel)
		return true
	}

	if message.Command && parts[0] == "reguntappd" {
		chanNick := message.User.Name
		channel := message.Channel

		if len(parts) < 2 {
			p.b.Send(c, bot.Message, channel, "You must also provide a user name.")
		} else if len(parts) == 3 {
			chanNick = parts[2]
		} else if len(parts) == 4 {
			chanNick = parts[2]
			channel = parts[3]
		}
		u := untappdUser{
			untappdUser: parts[1],
			chanNick:    chanNick,
			channel:     channel,
		}

		log.Info().
			Str("untappdUser", u.untappdUser).
			Str("nick", u.chanNick).
			Msg("Creating Untappd user")

		var count int
		err := p.db.QueryRow(`select count(*) from untappd
			where untappdUser = ?`, u.untappdUser).Scan(&count)
		if err != nil {
			log.Error().Err(err).Msgf("Error registering untappd")
		}
		if count > 0 {
			p.b.Send(c, bot.Message, channel, "I'm already watching you.")
			return true
		}
		_, err = p.db.Exec(`insert into untappd (
			untappdUser,
			channel,
			lastCheckin,
			chanNick
		) values (?, ?, ?, ?);`,
			u.untappdUser,
			u.channel,
			0,
			u.chanNick,
		)
		if err != nil {
			log.Error().Err(err).Msgf("Error registering untappd")
			p.b.Send(c, bot.Message, channel, "I can't see.")
			return true
		}

		p.b.Send(c, bot.Message, channel, "I'll be watching you.")

		p.checkUntappd(c, channel)

		return true
	}

	if message.Command && parts[0] == "checkuntappd" {
		log.Info().
			Str("user", message.User.Name).
			Msgf("Checking untappd at request of user.")
		p.checkUntappd(c, channel)
		return true
	}

	return false
}

// Help responds to help requests. Every plugin must implement a help function.
func (p *BeersPlugin) help(c bot.Connector, kind bot.Kind, message msg.Message, args ...interface{}) bool {
	msg := "Beers: imbibe by using either beers +=,=,++ or with the !imbibe/drink " +
		"commands. I'll keep a count of how many beers you've had and then if you want " +
		"to reset, just !puke it all up!"
	p.b.Send(c, bot.Message, message.Channel, msg)
	return true
}

func getUserBeers(db *sqlx.DB, user string) counter.Item {
	booze, _ := counter.GetUserItem(db, user, itemName)
	return booze
}

func (p *BeersPlugin) setBeers(user string, amount int) {
	ub := getUserBeers(p.db, user)
	err := ub.Update(amount)
	if err != nil {
		log.Error().Err(err).Msgf("Error saving beers")
	}
}

func (p *BeersPlugin) addBeers(user string, delta int) {
	ub := getUserBeers(p.db, user)
	err := ub.UpdateDelta(delta)
	if err != nil {
		log.Error().Err(err).Msgf("Error saving beers")
	}
}

func (p *BeersPlugin) getBeers(nick string) int {
	ub := getUserBeers(p.db, nick)
	return ub.Count
}

func (p *BeersPlugin) reportCount(c bot.Connector, nick, channel string, himself bool) {
	beers := p.getBeers(nick)
	msg := fmt.Sprintf("%s has had %d beers so far.", nick, beers)
	if himself {
		if beers == 0 {
			msg = fmt.Sprintf("You really need to get drinkin, %s!", nick)
		} else {
			msg = fmt.Sprintf("You've had %d beers so far, %s.", beers, nick)
		}
	}
	p.b.Send(c, bot.Message, channel, msg)
}

func (p *BeersPlugin) puke(c bot.Connector, user string, channel string) {
	p.setBeers(user, 0)
	msg := fmt.Sprintf("Ohhhhhh, and a reversal of fortune for %s!", user)
	p.b.Send(c, bot.Message, channel, msg)
}

func (p *BeersPlugin) doIKnow(nick string) bool {
	var count int
	err := p.db.QueryRow(`select count(*) from beers where nick = ?`, nick).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// Sends random affirmation to the channel. This could be better (with a datastore for sayings)
func (p *BeersPlugin) randomReply(c bot.Connector, channel string) {
	replies := []string{"ZIGGY! ZAGGY!", "HIC!", "Stay thirsty, my friend!"}
	p.b.Send(c, bot.Message, channel, replies[rand.Intn(len(replies))])
}

type checkin struct {
	Checkin_id      int
	Created_at      string
	Checkin_comment string
	Rating_score    float64
	Beer            map[string]interface{}
	Brewery         map[string]interface{}
	Venue           interface{}
	User            mrUntappd
	Media           struct {
		Count int
		Items []struct {
			Photo_id int
			Photo    struct {
				Photo_img_sm string
				Photo_img_md string
				Photo_img_lg string
				Photo_img_og string
			}
		}
	}
	Badges struct {
		Count int
		Items []struct {
			BadgeName        string `json:"badge_name"`
			BadgeDescription string `json:"badge_description"`
			BadgeImage       struct {
				Sm string
				Md string
				Lg string
			} `json:"badge_image"`
		}
	}
}

type mrUntappd struct {
	Uid          int
	User_name    string
	Relationship string
}

type checkins struct {
	Count int
	Items []checkin
}

type resp struct {
	Checkins checkins
}

type Beers struct {
	Response resp
}

func (p *BeersPlugin) pullUntappd() ([]checkin, error) {
	token := p.c.Get("Untappd.Token", "NONE")
	if token == "NONE" || token == "" {
		return []checkin{}, fmt.Errorf("No untappd token")
	}

	access_token := "?access_token=" + token
	baseUrl := "https://api.untappd.com/v4/checkin/recent/"

	url := baseUrl + access_token + "&limit=25"

	resp, err := http.Get(url)
	if err != nil {
		return []checkin{}, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []checkin{}, err
	}

	if resp.StatusCode == 500 {
		log.Error().Msgf("Error querying untappd: %s, %s", resp.Status, body)
		return []checkin{}, errors.New(resp.Status)
	}

	var beers Beers
	err = json.Unmarshal(body, &beers)
	if err != nil {
		log.Error().Err(err)
		return []checkin{}, err
	}
	return beers.Response.Checkins.Items, nil
}

func (p *BeersPlugin) checkUntappd(c bot.Connector, channel string) {
	token := p.c.Get("Untappd.Token", "NONE")
	if token == "NONE" {
		log.Info().
			Msg(`Set config value "untappd.token" if you wish to enable untappd`)
		return
	}

	userMap := make(map[string]untappdUser)
	rows, err := p.db.Query(`select id, untappdUser, channel, lastCheckin, chanNick from untappd;`)
	if err != nil {
		log.Error().Err(err).Msg("Error getting untappd users")
		return
	}
	for rows.Next() {
		u := untappdUser{}
		err := rows.Scan(&u.id, &u.untappdUser, &u.channel, &u.lastCheckin, &u.chanNick)
		if err != nil {
			log.Fatal().Err(err)
		}
		userMap[u.untappdUser] = u
		if u.chanNick == "" {
			log.Fatal().Msg("Empty chanNick for no good reason.")
		}
	}

	chks, err := p.pullUntappd()
	if err != nil {
		log.Error().Err(err).Msg("Untappd ERROR")
		return
	}
	for i := len(chks); i > 0; i-- {
		checkin := chks[i-1]

		if checkin.Checkin_id <= userMap[checkin.User.User_name].lastCheckin {
			continue
		}

		user, ok := userMap[checkin.User.User_name]
		if !ok {
			continue
		}

		p.sendCheckin(c, channel, user, checkin)
	}
}

func (p *BeersPlugin) sendCheckin(c bot.Connector, channel string, user untappdUser, checkin checkin) {
	venue := ""
	switch v := checkin.Venue.(type) {
	case map[string]interface{}:
		venue = " at " + v["venue_name"].(string)
	}
	beerName := checkin.Beer["beer_name"].(string)
	breweryName := checkin.Brewery["brewery_name"].(string)

	log.Debug().
		Msgf("user.chanNick: %s, user.untappdUser: %s, checkin.User.User_name: %s",
			user.chanNick, user.untappdUser, checkin.User.User_name)

	args := []interface{}{}
	if checkin.Badges.Count > 0 {
		for _, b := range checkin.Badges.Items {
			args = append(args, bot.ImageAttachment{
				URL:    b.BadgeImage.Sm,
				AltTxt: b.BadgeName,
			})
		}
	}
	if checkin.Media.Count > 0 {
		if strings.Contains(checkin.Media.Items[0].Photo.Photo_img_lg, "photos-processing") {
			return
		}
		mediaURL := p.getMedia(checkin.Media.Items[0].Photo.Photo_img_lg)
		args = append(args, bot.ImageAttachment{
			URL:    mediaURL,
			AltTxt: "Here's a photo",
		})
	} else if !p.untapdCache[checkin.Checkin_id] {
		// Mark checkin as "seen" but not complete, continue to next checkin
		log.Debug().Msgf("Deferring checkin: %#v", checkin)
		p.untapdCache[checkin.Checkin_id] = true
		return
	} else {
		// We've seen this checkin, so unmark and accept that there's no media
		delete(p.untapdCache, checkin.Checkin_id)
	}

	// Don't add beers till after a photo has been detected (or failed once)
	p.addBeers(user.chanNick, 1)
	drunken := p.getBeers(user.chanNick)

	msg := fmt.Sprintf("%s just drank %s by %s%s, bringing his drunkeness to %d",
		user.chanNick, beerName, breweryName, venue, drunken)
	if checkin.Rating_score > 0 {
		msg = fmt.Sprintf("%s. Rating: %.2f", msg, checkin.Rating_score)
	}
	if checkin.Checkin_comment != "" {
		msg = fmt.Sprintf("%s -- %s",
			msg, checkin.Checkin_comment)
	}

	args = append([]interface{}{channel, msg}, args...)

	user.lastCheckin = checkin.Checkin_id
	_, err := p.db.Exec(`update untappd set
			lastCheckin = ?
		where id = ?`, user.lastCheckin, user.id)
	if err != nil {
		log.Error().Err(err).Msg("UPDATE ERROR!")
	}

	log.Debug().
		Int("checkin_id", checkin.Checkin_id).
		Str("msg", msg).
		Interface("args", args).
		Msg("checkin")

	p.b.Send(c, bot.Message, args...)
}

func (p *BeersPlugin) getMedia(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return src
	}

	img, err := downloadMedia(u)
	if err != nil {
		return src
	}

	r := img.Bounds()
	w := r.Dx()
	h := r.Dy()

	maxSz := p.c.GetFloat64("maxImgSz", 750.0)

	if w > h {
		scale := maxSz / float64(w)
		w = int(float64(w) * scale)
		h = int(float64(h) * scale)
	} else {
		scale := maxSz / float64(h)
		w = int(float64(w) * scale)
		h = int(float64(h) * scale)
	}

	log.Debug().Msgf("trynig to resize to %v, %v", w, h)
	img = resize.Resize(uint(w), uint(h), img, resize.Lanczos3)
	r = img.Bounds()
	w = r.Dx()
	h = r.Dy()
	log.Debug().Msgf("resized to %v, %v", w, h)

	buf := bytes.Buffer{}
	err = png.Encode(&buf, img)
	if err != nil {
		return src
	}

	baseURL := p.c.Get("BaseURL", `https://catbase.velour.ninja`)
	id := uuid.New().String()

	cachedImages[id] = buf.Bytes()

	u, _ = url.Parse(baseURL)
	u.Path = path.Join(u.Path, "beers", "img", id)

	log.Debug().Msgf("New image at %s", u)

	return u.String()
}

func downloadMedia(u *url.URL) (image.Image, error) {
	res, err := http.Get(u.String())
	if err != nil {
		log.Error().Msgf("template from %s failed because of %v", u.String(), err)
		return nil, err
	}
	defer res.Body.Close()
	image, _, err := image.Decode(res.Body)
	if err != nil {
		log.Error().Msgf("Could not decode %v because of %v", u, err)
		return nil, err
	}
	return image, nil
}

func (p *BeersPlugin) untappdLoop(c bot.Connector, channel string) {
	frequency := p.c.GetInt("Untappd.Freq", 120)
	if frequency == 0 {
		return
	}

	log.Info().Msgf("Checking every %v seconds", frequency)

	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		p.checkUntappd(c, channel)
	}
}

func (p *BeersPlugin) registerWeb() {
	http.HandleFunc("/beers/img/", p.img)
}

func (p *BeersPlugin) img(w http.ResponseWriter, r *http.Request) {
	_, file := path.Split(r.URL.Path)
	id := file
	if img, ok := cachedImages[id]; ok {
		w.Write(img)
	} else {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}
}
