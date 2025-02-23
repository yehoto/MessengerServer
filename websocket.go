package main

import (
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
		db, err := connectDB()
		if err != nil {
			log.Println("Ошибка подключения к базе данных:", err)
			continue
		}
		defer db.Close()

		_, err = db.Exec("INSERT INTO messages (user_id, message) VALUES ($1, $2)", 1, string(message)) // user_id должен быть получен из сессии
		if err != nil {
			log.Println("Ошибка при сохранении сообщения:", err)
		}

		// Пересылаем сообщение всем клиентам
		clientsMu.Lock()
		for client := range clients {
			if client != conn {
				err := client.WriteMessage(websocket.TextMessage, message)
				if err != nil {
					log.Println("Ошибка при отправке сообщения:", err)
					client.Close()
					delete(clients, client)
				}
			}
		}
		clientsMu.Unlock()
	}

	clientsMu.Lock()
	delete(clients, conn)
	clientsMu.Unlock()
	fmt.Println("Клиент отключен")
}
