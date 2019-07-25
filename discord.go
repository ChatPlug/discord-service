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

	"github.com/ChatPlug/client-go"
	"github.com/bwmarrin/discordgo"
)

type DiscordService struct {
	client        *gql_client.ChatPlugClient
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

func (ds *DiscordService) Startup(args []string) {
	ds.client = &gql_client.ChatPlugClient{}
	ds.client.Connect(
		os.Getenv("INSTANCE_ID"),
		os.Getenv("HTTP_ENDPOINT"),
		os.Getenv("WS_ENDPOINT"),
	)

	if !ds.IsConfigured() {
		config := ds.client.AwaitConfiguration(ds.GetConfigurationSchema())
		ds.SaveConfiguration(config.FieldValues)
	}

	serviceConfiguration, err := ds.GetConfiguration()

	if err != nil {
		log.Fatal(err)
	}

	ds.discordClient, err = discordgo.New("Bot " + serviceConfiguration.BotToken)
	ds.discordClient.AddHandler(ds.discordMessageCreate)

	ds.discordClient.Open()
	msgChan := ds.client.SubscribeToNewMessages()
	defer ds.client.Close()

	for msg := range msgChan {
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

	attachments := make([]*gql_client.AttachmentInput, 0)

	for _, discordAttachment := range m.Attachments {
		attachment := gql_client.AttachmentInput{
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

func (ds *DiscordService) GetConfigurationSchema() []gql_client.ConfigurationField {
	conf := make([]gql_client.ConfigurationField, 0)
	ques1 := gql_client.ConfigurationField{
		Type:         "STRING",
		Hint:         "Your Discord bot token",
		DefaultValue: "",
		Optional:     false,
		Mask:         true,
	}
	conf = append(conf, ques1)
	return conf
}

func (ds *DiscordService) GetConfiguration() (*DiscordServiceConfiguration, error) {
	file, err := ioutil.ReadFile("config." + ds.client.InstanceID + ".json")

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

func (ds *DiscordService) SaveConfiguration(conf []string) {
	confStruct := DiscordServiceConfiguration{
		BotToken: conf[0],
	}

	file, _ := json.MarshalIndent(&confStruct, "", " ")

	_ = ioutil.WriteFile("config."+ds.client.InstanceID+".json", file, 0644)
}

func (ds *DiscordService) IsConfigured() bool {
	if _, err := os.Stat("config." + ds.client.InstanceID + ".json"); os.IsNotExist(err) {
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
