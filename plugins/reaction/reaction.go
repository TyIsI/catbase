// © 2013 the CatBase Authors under the WTFPL. See AUTHORS for the list of authors.

package reaction

import (
	"math/rand"

	"github.com/velour/catbase/bot"
	"github.com/velour/catbase/bot/msg"
	"github.com/velour/catbase/config"
)

type ReactionPlugin struct {
	Bot    bot.Bot
	Config *config.Config
}

func New(bot bot.Bot) *ReactionPlugin {
	return &ReactionPlugin{
		Bot:    bot,
		Config: bot.Config(),
	}
}

func (p *ReactionPlugin) Message(message msg.Message) bool {
	harrass := false
	for _, nick := range p.Config.GetArray("Reaction.HarrassList", []string{}) {
		if message.User.Name == nick {
			harrass = true
			break
		}
	}

	chance := p.Config.GetFloat64("Reaction.GeneralChance", 0.01)
	negativeWeight := 1
	if harrass {
		chance = p.Config.GetFloat64("Reaction.HarrassChance", 0.05)
		negativeWeight = p.Config.GetInt("Reaction.NegativeHarrassmentMultiplier", 2)
	}

	if rand.Float64() < chance {
		numPositiveReactions := len(p.Config.GetArray("Reaction.PositiveReactions", []string{}))
		numNegativeReactions := len(p.Config.GetArray("Reaction.NegativeReactions", []string{}))

		maxIndex := numPositiveReactions + numNegativeReactions*negativeWeight

		index := rand.Intn(maxIndex)

		reaction := ""

		if index < numPositiveReactions {
			reaction = p.Config.GetArray("Reaction.PositiveReactions", []string{})[index]
		} else {
			index -= numPositiveReactions
			index %= numNegativeReactions
			reaction = p.Config.GetArray("Reaction.NegativeReactions", []string{})[index]
		}

		p.Bot.Send(bot.Reaction, message.Channel, reaction, message)
	}

	return false
}

func (p *ReactionPlugin) Help(channel string, parts []string) {

}

func (p *ReactionPlugin) Event(kind string, message msg.Message) bool {
	return false
}

func (p *ReactionPlugin) BotMessage(message msg.Message) bool {
	return false
}

func (p *ReactionPlugin) RegisterWeb() *string {
	return nil
}

func (p *ReactionPlugin) ReplyMessage(message msg.Message, identifier string) bool { return false }
