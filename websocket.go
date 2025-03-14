package main

import (
	"encoding/json"
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

// Добавляем структуру для хранения информации о чатах
var chatParticipants = make(map[int][]int) // chatID -> []userID
var chatParticipantsMu sync.Mutex

// Обновляем статус пользователя при подключении/отключении
func updateUserStatus(userID int, online bool) {
	userStatusMu.Lock()
	defer userStatusMu.Unlock()
	userStatuses[userID] = online
}

// Отправляем статус пользователя всем клиентам
// Отправляем статус пользователя только тем, кто находится в том же чате
func broadcastUserStatus(userID int, online bool, chatID int) {
	statusMessage := map[string]interface{}{
		"type":    "user_status",
		"user_id": userID,
		"online":  online,
	}

	chatParticipantsMu.Lock()
	participants := chatParticipants[chatID]
	chatParticipantsMu.Unlock()

	clientsMu.Lock()
	defer clientsMu.Unlock()
	for client := range clients {
		// Проверяем, находится ли клиент в том же чате
		if contains(participants, userID) {
			err := client.WriteJSON(statusMessage)
			if err != nil {
				log.Println("Ошибка при отправке статуса:", err)
				client.Close()
				delete(clients, client)
			}
		}
	}
}

// Вспомогательная функция для проверки наличия элемента в слайсе
func contains(slice []int, item int) bool {
	for _, a := range slice {
		if a == item {
			return true
		}
	}
	return false
}

// handleWebSocket обрабатывает WebSocket-соединения
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Ошибка при обновлении соединения:", err)
		return
	}
	defer conn.Close()

	// Получаем user_id и chat_id из запроса
	userIDStr := r.URL.Query().Get("user_id")
	chatIDStr := r.URL.Query().Get("chat_id")
	// Проверяем наличие параметров
	if userIDStr == "" || chatIDStr == "" {
		log.Println("Missing parameters: user_id or chat_id")
		conn.WriteMessage(websocket.CloseMessage, []byte("400 Bad Request"))
		return
	}
	// Конвертируем в числа
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		log.Println("Invalid user_id:", err)
		conn.WriteMessage(websocket.CloseMessage, []byte("400 Invalid user_id"))
		return
	}

	chatID, err := strconv.Atoi(chatIDStr)
	if err != nil {
		log.Println("Invalid chat_id:", err)
		conn.WriteMessage(websocket.CloseMessage, []byte("400 Invalid chat_id"))
		return
	}

	log.Printf("New connection: user_id=%d, chat_id=%d", userID, chatID)

	// Обновляем участников чата
	chatParticipantsMu.Lock()
	chatParticipants[chatID] = append(chatParticipants[chatID], userID)
	chatParticipantsMu.Unlock()

	// Обновляем статус пользователя на "онлайн"
	updateUserStatus(userID, true)
	broadcastUserStatus(userID, true, chatID)

	// Добавляем соединение в список клиентов
	clientsMu.Lock()
	clients[conn] = true
	clientsMu.Unlock()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("Ошибка при чтении сообщения:", err)
			break
		}

		// Декодируем сообщение
		var msgData struct {
			ChatID int    `json:"chat_id"`
			UserID int    `json:"user_id"`
			Text   string `json:"text"`
		}
		if err := json.Unmarshal(message, &msgData); err != nil {
			log.Println("Ошибка декодирования сообщения:", err)
			continue
		}

		// Сохраняем сообщение в базе данных
		db, err := connectDB()
		if err != nil {
			log.Println("Ошибка подключения к базе данных:", err)
			continue
		}
		defer db.Close()

		var messageID int
		err = db.QueryRow(
			"INSERT INTO messages (chat_id, user_id, content) VALUES ($1, $2, $3) RETURNING id",
			msgData.ChatID,
			msgData.UserID,
			msgData.Text,
		).Scan(&messageID)
		if err != nil {
			log.Println("Ошибка при сохранении сообщения:", err)
			continue
		}

		// Пересылаем сообщение всем участникам чата
		chatParticipantsMu.Lock()
		participants := chatParticipants[msgData.ChatID]
		chatParticipantsMu.Unlock()

		clientsMu.Lock()
		for client := range clients {
			// Проверяем, находится ли клиент в том же чате
			if contains(participants, msgData.UserID) {
				err := client.WriteJSON(map[string]interface{}{
					"id":         messageID,
					"chat_id":    msgData.ChatID,
					"user_id":    msgData.UserID,
					"text":       msgData.Text,
					"created_at": time.Now().Format(time.RFC3339),
					"isMe":       (client == conn),
				})
				if err != nil {
					log.Println("Ошибка при отправке сообщения:", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
		clientsMu.Unlock()
	}

	// Обновляем статус пользователя на "оффлайн" при отключении
	updateUserStatus(userID, false)
	broadcastUserStatus(userID, false, chatID)

	clientsMu.Lock()
	delete(clients, conn)
	clientsMu.Unlock()
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
