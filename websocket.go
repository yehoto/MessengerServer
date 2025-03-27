package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Настройка WebSocket апгрейдера с разрешением всех Origin (только для разработки!)
// websocket.Upgrader - структура из пакета gorilla/websocket, которая:
// Конвертирует обычное HTTP-соединение в WebSocket
// Настраивает параметры "рукопожатия" (handshake)
// Контролирует политики безопасности

// Origin - HTTP-заголовок, который браузеры автоматически добавляют к WebSocket-запросам
// Указывает домен, с которого пришел запрос (например: https://my-site.com)
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		log.Printf("Разрешен запрос от origin: %s", r.Header.Get("Origin"))
		return true
	},
}

type clientInfo struct {
	userID int
	chatID int
}

var clients = make(map[*websocket.Conn]clientInfo)

// Хранилище подключенных клиентов и мьютекс для безопасности
// var clients = make(map[*websocket.Conn]bool) //bool - соединение активно/нет, но в реализации особо не нужно
// Мьютекс — это примитив синхронизации, который позволяет:
// Блокировать доступ к данным, если их использует другая горутина.
// Разблокировать доступ, когда работа с данными завершена.
var clientsMu sync.Mutex

// Хранилище статусов пользователей и мьютекс
var (
	userStatuses = make(map[int]bool)
	userStatusMu sync.Mutex
)

// Обновление статуса пользователя с логированием
func updateUserStatus(userID int, online bool) {
	userStatusMu.Lock()         //блокируем доступ к мапе для другоих горутин
	defer userStatusMu.Unlock() //defer откладывает выполнение указанной функции до момента, когда текущая функция завершится
	prevStatus := userStatuses[userID]
	userStatuses[userID] = online
	log.Printf("Статус пользователя %d изменен: %t -> %t", userID, prevStatus, online)
}

// Рассылка статуса пользователя всем клиентам
func broadcastUserStatus(userID int, online bool) {
	statusMessage := map[string]interface{}{ //Тип interface{} в Go используется для создания переменных, которые могут хранить значения любого типа.
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	log.Printf("Рассылка статуса пользователя %d (%t) для %d клиентов", userID, online, len(clients))

	for client := range clients {
		err := client.WriteJSON(statusMessage)
		if err != nil {
			log.Printf("Ошибка отправки [%d]: %v", userID, err)
			client.Close()
			delete(clients, client)
		}
	}
}

// Обработчик WebSocket соединений
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Ошибка апгрейда WebSocket: %v", err)
		return
	}
	defer conn.Close()

	userIDStr := r.URL.Query().Get("user_id")
	userID, _ := strconv.Atoi(userIDStr)
	chatIDStr := r.URL.Query().Get("chat_id")
	chatID, _ := strconv.Atoi(chatIDStr)
	log.Printf("Подключение WebSocket: user_id=%d, chat_id=%d", userID, chatID)

	clientsMu.Lock()
	clients[conn] = clientInfo{userID: userID, chatID: chatID}
	clientsMu.Unlock()

	updateUserStatus(userID, true)
	broadcastUserStatus(userID, true)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Ошибка чтения сообщения WebSocket: %v", err)
			break
		}
		log.Printf("Получено сообщение через WebSocket: %s", string(message))

		var msgData struct {
			ChatID           int    `json:"chat_id"`
			UserID           int    `json:"user_id"`
			Text             string `json:"text"`
			ParentMessageID  *int   `json:"parent_message_id"`
			IsForwarded      bool   `json:"is_forwarded"`
			OriginalSenderID *int   `json:"original_sender_id"`
			OriginalChatID   *int   `json:"original_chat_id"`
		}
		if err := json.Unmarshal(message, &msgData); err != nil {
			log.Printf("Ошибка парсинга сообщения WebSocket: %v", err)
			continue
		}

		db, err := connectDB()
		if err != nil {
			log.Printf("Ошибка подключения к БД в WebSocket: %v", err)
			continue
		}
		defer db.Close()

		var parentMessageID sql.NullInt64
		if msgData.ParentMessageID != nil {
			parentMessageID.Int64 = int64(*msgData.ParentMessageID)
			parentMessageID.Valid = true
		}
		var originalSenderID sql.NullInt64
		if msgData.OriginalSenderID != nil {
			originalSenderID.Int64 = int64(*msgData.OriginalSenderID)
			originalSenderID.Valid = true
		}
		var originalChatID sql.NullInt64
		if msgData.OriginalChatID != nil {
			originalChatID.Int64 = int64(*msgData.OriginalChatID)
			originalChatID.Valid = true
		}

		var messageID int
		err = db.QueryRow(
			"INSERT INTO messages (chat_id, user_id, content, parent_message_id, is_forwarded, original_sender_id, original_chat_id) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id",
			msgData.ChatID, msgData.UserID, msgData.Text, parentMessageID, msgData.IsForwarded, originalSenderID, originalChatID,
		).Scan(&messageID)
		if err != nil {
			log.Printf("Ошибка сохранения сообщения в БД: %v", err)
			continue
		}
		log.Printf("Сообщение сохранено с ID: %d", messageID)

		var senderName string
		err = db.QueryRow("SELECT username FROM users WHERE id = $1", msgData.UserID).Scan(&senderName)
		if err != nil {
			log.Printf("Ошибка получения имени отправителя: %v", err)
			senderName = "Unknown"
		}

		msgDataMap := map[string]interface{}{
			"id":                 messageID,
			"chat_id":            msgData.ChatID,
			"user_id":            msgData.UserID,
			"text":               msgData.Text,
			"created_at":         time.Now().Format(time.RFC3339),
			"isMe":               false,
			"sender_name":        senderName,
			"parent_message_id":  msgData.ParentMessageID,
			"is_forwarded":       msgData.IsForwarded,
			"original_sender_id": msgData.OriginalSenderID,
			"original_chat_id":   msgData.OriginalChatID,
		}
		if msgData.ParentMessageID != nil {
			var parentContent string
			err := db.QueryRow("SELECT content FROM messages WHERE id = $1", *msgData.ParentMessageID).Scan(&parentContent)
			if err == nil {
				msgDataMap["parent_content"] = parentContent
			} else {
				log.Printf("Ошибка получения parent_content: %v", err)
			}
		}

		clientsMu.Lock()
		log.Printf("Рассылка сообщения клиентам в чате %d", msgData.ChatID)
		for client, info := range clients {
			if info.chatID == msgData.ChatID {
				isMe := (client == conn)
				msgDataMap["isMe"] = isMe
				messageWithIsMe, _ := json.Marshal(msgDataMap)
				err := client.WriteMessage(websocket.TextMessage, messageWithIsMe)
				if err != nil {
					log.Printf("Ошибка отправки сообщения клиенту: %v", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
		clientsMu.Unlock()
	}

	log.Printf("Клиент отключен: user_id=%d", userID)
	updateUserStatus(userID, false)
	broadcastUserStatus(userID, false)
	clientsMu.Lock()
	delete(clients, conn)
	clientsMu.Unlock()
}

// HTTP обработчик для проверки статуса
func getUserStatusHandler(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.URL.Query().Get("user_id")
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		log.Printf("Невалидный user_id в запросе: %s", userIDStr)
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

	userStatusMu.Lock()
	online := userStatuses[userID]
	userStatusMu.Unlock()

	log.Printf("Запрос статуса [%d]: %t", userID, online)
	json.NewEncoder(w).Encode(map[string]bool{"online": online})
}

// Отправка статуса конкретному клиенту
func sendUserStatus(conn *websocket.Conn, userID int, online bool) {
	statusMessage := map[string]interface{}{
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	log.Printf("Отправка статуса [%d:%t] клиенту [%v]", userID, online, conn.RemoteAddr())
	err := conn.WriteJSON(statusMessage)
	if err != nil {
		log.Printf("Ошибка отправки статуса [%d]: %v", userID, err)
		conn.Close()
		delete(clients, conn)
	}
}
