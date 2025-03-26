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

// Хранилище подключенных клиентов и мьютекс для безопасности
var clients = make(map[*websocket.Conn]bool) //bool - соединение активно/нет, но в реализации особо не нужно
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
	// Обновление соединения до WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)

	if err != nil {
		log.Printf("Ошибка апгрейда: %v", err) //%v подставит значение err в строку
		return
	}
	defer conn.Close()

	// Извлечение user_id из query параметров
	userIDStr := r.URL.Query().Get("user_id")
	//онвертирование в число
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		log.Printf("Невалидный user_id: %s | Ошибка: %v", userIDStr, err)
		return
	}

	log.Printf("Новое подключение | UserID: %d | IP: %s", userID, r.RemoteAddr)

	// Обновление статуса пользователя
	updateUserStatus(userID, true)
	broadcastUserStatus(userID, true)

	// Отправка текущих статусов новому клиенту
	userStatusMu.Lock()
	log.Printf("Отправка истории статусов для %d (всего: %d)", userID, len(userStatuses))
	for otherUserID, online := range userStatuses {
		if otherUserID != userID {
			sendUserStatus(conn, otherUserID, online)
		}
	}
	userStatusMu.Unlock()

	// Регистрация клиента
	clientsMu.Lock()
	clients[conn] = true
	clientsMu.Unlock()

	// Главный цикл обработки сообщений
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Ошибка чтения [%d]: %v", userID, err)
			break
		}

		log.Printf("Получено сообщение [%d]: %s", userID, string(message))

		// Парсинг сообщения
		var msgData struct {
			ChatID int    `json:"chat_id"`
			UserID int    `json:"user_id"`
			Text   string `json:"text"`
		}

		if err := json.Unmarshal(message, &msgData); err != nil {
			log.Printf("Ошибка парсинга [%d]: %v", userID, err)
			continue
		}

		// Сохранение в БД
		db, err := connectDB()
		if err != nil {
			log.Printf("Ошибка подключения к БД [%d]: %v", userID, err)
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
			log.Printf("Ошибка сохранения [%d]: %v", userID, err)
		}

		// После вставки сообщения в БД
		var senderName string
		err = db.QueryRow("SELECT username FROM users WHERE id = $1", msgData.UserID).Scan(&senderName)
		if err != nil {
			log.Printf("Ошибка получения имени: %v", err)
			senderName = "Unknown" // Запасной вариант
		}

		// Подготовка сообщения для рассылки
		msgDataMap := map[string]interface{}{
			"id":          messageID,
			"chat_id":     msgData.ChatID,
			"user_id":     msgData.UserID,
			"text":        msgData.Text,
			"created_at":  time.Now().Format(time.RFC3339),
			"isMe":        false,
			"sender_name": senderName,
		}

		// Рассылка сообщения
		clientsMu.Lock()
		log.Printf("Рассылка сообщения [chat:%d] от [user:%d]", msgData.ChatID, userID)
		for client := range clients {
			isMe := (client == conn)
			msgDataMap["isMe"] = isMe

			messageWithIsMe, _ := json.Marshal(msgDataMap)
			err := client.WriteMessage(websocket.TextMessage, messageWithIsMe)
			if err != nil {
				log.Printf("Ошибка отправки [%d]: %v", userID, err)
				client.Close()
				delete(clients, client)
			}
		}
		clientsMu.Unlock()
	}

	// Обработка отключения клиента
	defer func() {
		log.Printf("Отключение [%d] | Продолжительность: %s", userID)
		updateUserStatus(userID, false)
		broadcastUserStatus(userID, false)

		clientsMu.Lock()
		delete(clients, conn)
		clientsMu.Unlock()
	}()
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
