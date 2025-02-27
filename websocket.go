package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var clients = make(map[*websocket.Conn]bool)
var clientsMu sync.Mutex

// handleWebSocket обрабатывает WebSocket-соединения
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Ошибка при обновлении соединения:", err)
		return
	}
	defer conn.Close()

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
			_, err = db.Exec(
				"INSERT INTO messages (chat_id, user_id, content) VALUES ($1, $2, $3)",
				msgData.ChatID,
				msgData.UserID,
				msgData.Text,
			)
			if err != nil {
				log.Println("Ошибка при сохранении сообщения:", err)
			}

			// Добавляем isMe в сообщение
			msgDataMap := map[string]interface{}{
				"chat_id": msgData.ChatID,
				"user_id": msgData.UserID,
				"text":    msgData.Text,
				"isMe":    false, // По умолчанию false, так как это сообщение от другого пользователя
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

	clientsMu.Lock()
	delete(clients, conn)
	clientsMu.Unlock()
	fmt.Println("Клиент отключен")
}
