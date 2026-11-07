package models

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/siwc"
)

// ModeChatGPTPlan marks models billed to the user's ChatGPT plan through Sign
// in with ChatGPT. (Mode "chatgpt" is the exe.dev gateway's route.)
const ModeChatGPTPlan = "chatgpt-plan"

const chatGPTPlanPrefix = "chatgpt:"

// ChatGPTPlanSourceID is the model source of a signed-in ChatGPT account.
func ChatGPTPlanSourceID(accountID string) string { return chatGPTPlanPrefix + accountID }

// ChatGPTPlanRouteID names a plan model of one account.
func ChatGPTPlanRouteID(accountID, slug string) string {
	return chatGPTPlanPrefix + accountID + ":" + base64.RawURLEncoding.EncodeToString([]byte(slug))
}

// SetChatGPTPlans connects signed-in ChatGPT accounts' models. Call
// RefreshChatGPTPlans when accounts change.
func (m *Manager) SetChatGPTPlans(plans *siwc.Manager) {
	m.mu.Lock()
	m.chatGPT = plans
	m.mu.Unlock()
}

// RefreshChatGPTPlans lists the models of accounts that allow plan usage.
func (m *Manager) RefreshChatGPTPlans() error {
	m.mu.RLock()
	plans := m.chatGPT
	m.mu.RUnlock()
	if plans == nil {
		return nil
	}
	accounts, err := plans.Accounts(context.Background())
	if err != nil {
		return err
	}
	fresh := make(map[string]serviceEntry)
	var order []string
	for _, account := range accounts {
		if !account.PlanUsage {
			continue
		}
		for _, model := range account.Models {
			entry, err := m.chatGPTPlanEntry(plans, account, model.Slug)
			if err != nil {
				return err
			}
			fresh[entry.modelID] = entry
			order = append(order, entry.modelID)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := append([]string(nil), m.modelOrder...)
	kept := m.modelOrder[:0]
	for _, id := range m.modelOrder {
		if strings.HasPrefix(m.services[id].sourceID, chatGPTPlanPrefix) {
			delete(m.services, id)
		} else {
			kept = append(kept, id)
		}
	}
	m.modelOrder = append(kept, order...)
	for id, entry := range fresh {
		m.services[id] = entry
	}
	m.preserveProviderOrder(previous)
	return nil
}

// chatGPTPlanEntry configures a model like an OpenAI Responses provider
// model, then sends it through the account's plan.
func (m *Manager) chatGPTPlanEntry(plans *siwc.Manager, account siwc.AccountInfo, slug string) (serviceEntry, error) {
	name := slug
	for _, model := range account.Models {
		if model.Slug == slug {
			name = model.DisplayName
		}
	}
	entry, err := m.providerEntry(db.ModelProvider{
		ProviderID: ChatGPTPlanSourceID(account.ID), ProviderType: "openai-responses", Endpoint: plans.API(),
		Models: []db.ProviderModel{{ID: slug, Name: name}},
	}, slug)
	if err != nil {
		return serviceEntry{}, err
	}
	raw, ok := entry.service.(*reasoningService).Service.(*oai.ResponsesService)
	if !ok {
		return serviceEntry{}, fmt.Errorf("ChatGPT plan model %s is not a Responses model", slug)
	}
	raw.ChatGPTPlan = &oai.ChatGPTPlan{Account: account.ID, Credentials: plans.Credentials(account.ID)}
	entry.modelID = ChatGPTPlanRouteID(account.ID, slug)
	entry.serviceRevision = ConnectionRevision(ModeChatGPTPlan, plans.API(), account.ID, slug)
	entry.mode = ModeChatGPTPlan
	entry.source = "ChatGPT · " + account.Label
	return entry, nil
}
