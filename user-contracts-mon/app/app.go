package app

import (
	"flag"
	"fmt"
	"log"
	"os"

	tgapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	monitor "github.com/threefoldtech/tfgrid-sdk-go/user-contracts-mon/internal"
)

// set at build time
var commit string
var version string

func Start() error {
	flag.NewFlagSet("version", flag.ExitOnError)
	if len(os.Args) > 1 {
		if os.Args[1] == "version" {
			fmt.Println(version)
			fmt.Println(commit)
			return nil
		}
	}

	env := ""
	flag.StringVar(&env, "e", "", "Path to env file")
	flag.Parse()

	conf, err := monitor.ParseConfig(env)
	if err != nil {
		return err
	}

	mon, err := monitor.NewMonitor(conf)
	if err != nil {
		return err
	}
	log.Printf("monitoring bot has started and waiting for requests")

	addChatChan := make(chan monitor.User)
	stopChatChan := make(chan int64)
	go mon.StartMonitoring(addChatChan, stopChatChan)

	u := tgapi.NewUpdate(0)
	u.Timeout = 60
	updates := mon.Bot.GetUpdatesChan(u)

	for update := range updates {

		if update.Message == nil {
			continue
		}
		switch update.Message.Text {

		case "/start":
			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)
			msg := tgapi.NewMessage(update.FromChat().ID, "Please send your network and mnemonic in the form\nnetwork=<network>\nmnemonic=<mnemonic>")
			_, err := mon.Bot.Send(msg)
			if err != nil {
				log.Println(err)
			}

		case "/stop":
			log.Printf("[%s] %s", update.Message.From.UserName, update.Message.Text)
			stopChatChan <- update.FromChat().ID

		default:
			user, err := monitor.NewUser(update)
			if err != nil {
				msg := tgapi.NewMessage(update.FromChat().ID, err.Error())
				_, err := mon.Bot.Send(msg)
				if err != nil {
					log.Println(err)
				}

				continue
			}
			addChatChan <- user
		}
	}
	return nil
}
