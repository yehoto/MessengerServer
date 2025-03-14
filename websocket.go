package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var clients = make(map[*websocket.Conn]bool)
var clientsMu sync.Mutex

var (
	userStatuses = make(map[int]bool) // Карта для хранения статусов пользователей
	userStatusMu sync.Mutex           // Мьютекс для безопасного доступа к карте
)

// Обновляем статус пользователя при подключении/отключении
func updateUserStatus(userID int, online bool) {
	userStatusMu.Lock()
	defer userStatusMu.Unlock()
	userStatuses[userID] = online
}

// Отправляем статус пользователя всем клиентам
func broadcastUserStatus(userID int, online bool) {
	statusMessage := map[string]interface{}{
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	clientsMu.Lock()
	defer clientsMu.Unlock()
	for client := range clients {
		err := client.WriteJSON(statusMessage)
		if err != nil {
			log.Println("Ошибка при отправке статуса:", err)
			client.Close()
			delete(clients, client)
		}
	}
}

// handleWebSocket обрабатывает WebSocket-соединения
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Ошибка при обновлении соединения:", err)
		return
	}
	defer conn.Close()

	// Получаем user_id из запроса
	userIDStr := r.URL.Query().Get("user_id")
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		log.Println("Ошибка при получении user_id:", err)
		return
	}

	// Обновляем статус пользователя на "онлайн"
	updateUserStatus(userID, true)
	broadcastUserStatus(userID, true)
	// Отправляем текущие статусы всех пользователей, с которыми ведется чат
	userStatusMu.Lock()
	for otherUserID, online := range userStatuses {
		if otherUserID != userID { // Не отправляем статус самому себе
			sendUserStatus(conn, otherUserID, online)
		}
	}
	userStatusMu.Unlock()

	clientsMu.Lock()
	clients[conn] = true
	clientsMu.Unlock()

	fmt.Println("Новый клиент подключен")

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("Ошибка при чтении сообщения:", err)
			break
		}

		fmt.Printf("Получено сообщение: %s\n", message)

		// Сохраняем сообщение в базе данных
		// В handleWebSocket
		var msgData struct {
			ChatID int    `json:"chat_id"`
			UserID int    `json:"user_id"`
			Text   string `json:"text"`
		}

		if err := json.Unmarshal(message, &msgData); err == nil {
			db, err := connectDB()
			if err != nil {
				log.Println("Ошибка подключения к базе данных:", err)
				return
			}
			defer db.Close()

			// Сохраняем сообщение в базе данных
			var messageID int
			err = db.QueryRow(
				"INSERT INTO messages (chat_id, user_id, content) VALUES ($1, $2, $3) RETURNING id",
				msgData.ChatID,
				msgData.UserID,
				msgData.Text,
			).Scan(&messageID)
			if err != nil {
				log.Println("Ошибка при сохранении сообщения:", err)
			}

			// Добавляем isMe в сообщение
			msgDataMap := map[string]interface{}{
				"id":           messageID,
				"chat_id":      msgData.ChatID,
				"user_id":      msgData.UserID,
				"text":         msgData.Text,
				"created_at":   time.Now().Format(time.RFC3339),
				"delivered_at": nil,   // Пока не доставлено
				"read_at":      nil,   // Пока не прочитано
				"isMe":         false, // По умолчанию false, так как это сообщение от другого пользователя
			}

			// Пересылаем сообщение только клиентам в том же чате
			clientsMu.Lock()
			for client := range clients {
				// Определяем, является ли текущий клиент отправителем
				isMe := (client == conn)
				msgDataMap["isMe"] = isMe // Добавляем флаг
				//if client != conn {
				// Кодируем сообщение с isMe
				messageWithIsMe, _ := json.Marshal(msgDataMap)
				err := client.WriteMessage(websocket.TextMessage, messageWithIsMe)
				if err != nil {
					log.Println("Ошибка при отправке сообщения:", err)
					client.Close()
					delete(clients, client)
				}
				//	}
			}
			clientsMu.Unlock()
		}
	}

	// Обновляем статус пользователя на "оффлайн" при отключении
	updateUserStatus(userID, false)
	broadcastUserStatus(userID, false)

	clientsMu.Lock()
	delete(clients, conn)
	clientsMu.Unlock()
	fmt.Println("Клиент отключен")
}

func getUserStatusHandler(w http.ResponseWriter, r *http.Request) {
	userIDStr := r.URL.Query().Get("user_id")
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		http.Error(w, "Invalid user_id", http.StatusBadRequest)
		return
	}

	userStatusMu.Lock()
	online := userStatuses[userID]
	userStatusMu.Unlock()

	json.NewEncoder(w).Encode(map[string]bool{"online": online})
}

func sendUserStatus(conn *websocket.Conn, userID int, online bool) {
	statusMessage := map[string]interface{}{
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	err := conn.WriteJSON(statusMessage)
	if err != nil {
		log.Println("Ошибка при отправке статуса:", err)
		conn.Close()
		delete(clients, conn)
	}
}
