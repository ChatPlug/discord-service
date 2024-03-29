package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strings"

	client "github.com/ChatPlug/client-go"
	"github.com/bwmarrin/discordgo"
)

type DiscordService struct {
	client        *client.ChatPlugClient
	discordClient *discordgo.Session
}

type WebhookPayload struct {
	Content   string `json:"content"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type DiscordServiceConfiguration struct {
	BotToken string `json:"botToken"`
}

func (ds *DiscordService) handleMessages() {

	for msg := range ds.client.MessagesChan {
		webhooks, _ := ds.discordClient.ChannelWebhooks(msg.TargetThreadID)

		hasWebhook := false
		var webhook *discordgo.Webhook

		for _, hook := range webhooks {
			if strings.HasPrefix(hook.Name, "ChatPlug ") {
				hasWebhook = true
				webhook = hook
			}
		}

		if !hasWebhook {
			channel, _ := ds.discordClient.Channel(msg.TargetThreadID)
			webhook, _ = ds.discordClient.WebhookCreate(msg.TargetThreadID, "ChatPlug "+channel.Name, "https://i.imgur.com/l2QP9Go.png")
		}

		url := fmt.Sprintf("https://discordapp.com/api/webhooks/%s/%s", webhook.ID, webhook.Token)
		payload, _ := json.Marshal(&WebhookPayload{
			Username:  msg.Message.Author.Username,
			AvatarURL: msg.Message.Author.AvatarURL,
			Content:   msg.Message.Body,
		})

		fmt.Println("Sending a message to the webhook")

		// http://polyglot.ninja/golang-making-http-requests/
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)

		payloadWriter, err := writer.CreateFormField("payload_json")
		if err != nil {
			log.Fatalln(err)
		}

		_, err = payloadWriter.Write([]byte(payload))
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Println("Wrote JSON payload")

		for _, attachment := range msg.Message.Attachments {
			filename := path.Base(attachment.SourceURL)

			fileWriter, err := writer.CreateFormFile(filename, filename)
			if err != nil {
				fmt.Println(err)
			}

			if err := DownloadFile(attachment.SourceURL, fileWriter); err != nil {
				fmt.Println(err)
				continue
			}
		}

		fmt.Println("Wrote attachments")

		writer.Close()

		req, err := http.NewRequest("POST", url, &body)
		if err != nil {
			fmt.Println(err)
		}
		// We need to set the content type from the writer, it includes necessary boundary as well
		req.Header.Set("Content-Type", writer.FormDataContentType())

		fmt.Println("Sending the request")
		// Do the request
		client := &http.Client{}
		response, err := client.Do(req)
		if err != nil {
			fmt.Println(err)
		}

		fmt.Println("Got response")
		if response.StatusCode != 204 && response.StatusCode != 200 {
			data, err := ioutil.ReadAll(response.Body)
			if err != nil {
				fmt.Println(err)
			}
			fmt.Println(data)
		}
	}
}

func (ds *DiscordService) Startup(args []string) {
	ds.client = client.NewChatPlugClient(os.Getenv("WS_ENDPOINT"), os.Getenv("HTTP_ENDPOINT"), os.Getenv("ACCESS_TOKEN"))
	ds.client.Connect()
	ds.client.SubscribeToNewMessages()
	defer ds.client.Close()

	if !ds.IsConfigured() {
		ds.client.SubscribeToConfigResponses(ds.GetConfigurationSchema())

		config := <-ds.client.ConfigurationRecvChan
		ds.SaveConfiguration(config.FieldValues)
	}

	serviceConfiguration, err := ds.GetConfiguration()

	if err != nil {
		log.Fatal(err)
	}

	ds.discordClient, err = discordgo.New("Bot " + serviceConfiguration.BotToken)
	ds.discordClient.AddHandler(ds.discordMessageCreate)

	_ = ds.discordClient.Open()
	ds.client.SubscribeToSearchRequests()

	go func() {
		ds.handleMessages()
	}()

	for searchRequest := range ds.client.SearchRequestsChan {
		threadResults := make([]*client.SearchThreadInput, 0)
		for _, guild := range ds.discordClient.State.Guilds {
			channels, _ := ds.discordClient.GuildChannels(guild.ID)
			for _, channel := range channels {
				if len(threadResults) < 30 && (strings.Contains(channel.Name, searchRequest.Query) || strings.Contains(guild.Name, searchRequest.Query)) && channel.Type == discordgo.ChannelTypeGuildText {
					threadResults = append(threadResults, &client.SearchThreadInput{
						Name:     guild.Name + " - " + channel.Name,
						IconURL:  "http://cdn.discordapp.com/icons/" + guild.ID + "/" + guild.Icon + ".png",
						OriginID: channel.ID,
					})
				}
			}
		}
		ds.client.SetSearchResponse(searchRequest.Query, threadResults)
	}
}

func (ds *DiscordService) discordMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}

	webhook, err := ds.discordClient.Webhook(m.WebhookID)
	if err == nil && webhook != nil {
		if strings.HasPrefix(webhook.Name, "ChatPlug ") {
			return
		}
	}

	attachments := make([]*client.AttachmentInput, 0)

	for _, discordAttachment := range m.Attachments {
		attachment := client.AttachmentInput{
			Type:      "IMAGE",
			OriginID:  discordAttachment.ID,
			SourceURL: discordAttachment.URL,
		}

		attachments = append(attachments, &attachment)
	}

	ds.client.SendMessage(
		m.Content,
		m.ID,
		m.ChannelID,
		m.Author.Username,
		m.Author.ID,
		m.Author.AvatarURL("medium"),
		attachments,
	)
}

func (ds *DiscordService) GetConfigurationSchema() []client.ConfigurationField {
	conf := make([]client.ConfigurationField, 0)
	ques1 := client.ConfigurationField{
		Type:         "STRING",
		Name:         "botToken",
		Hint:         "Your Discord bot token",
		DefaultValue: "",
		Optional:     false,
		Mask:         true,
	}
	conf = append(conf, ques1)
	return conf
}

func (ds *DiscordService) GetConfiguration() (*DiscordServiceConfiguration, error) {
	file, err := ioutil.ReadFile("config." + os.Getenv("INSTANCE_ID") + ".json")

	if err != nil {
		return nil, err
	}

	data := DiscordServiceConfiguration{}

	err = json.Unmarshal([]byte(file), &data)

	if err != nil {
		return nil, err
	}

	return &data, nil
}

func (ds *DiscordService) SaveConfiguration(conf []client.ConfigurationFieldResult) {
	confStruct := DiscordServiceConfiguration{}

	for _, field := range conf {
		if field.Name == "botToken" {
			confStruct.BotToken = field.Value
		}
	}

	file, _ := json.MarshalIndent(&confStruct, "", " ")

	_ = ioutil.WriteFile("config."+os.Getenv("INSTANCE_ID")+".json", file, 0644)
}

func (ds *DiscordService) IsConfigured() bool {
	if _, err := os.Stat("config." + os.Getenv("INSTANCE_ID") + ".json"); os.IsNotExist(err) {
		return false
	}
	return true
}

func DownloadFile(url string, dst io.Writer) error {
	filename := path.Base(url)

	head, err := http.Head(url)
	if err != nil {
		return err
	}
	if head.ContentLength > (8 * 1024 * 1024) {
		return fmt.Errorf("File %s too big", filename)
	}

	// https://golangcode.com/download-a-file-from-a-url/
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(dst, resp.Body)
	return err
}

func main() {
	client := DiscordService{}
	fmt.Println("Starting the service...")
	client.Startup(os.Args)
}
