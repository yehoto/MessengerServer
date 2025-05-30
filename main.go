package main

import (
	"crypto/tls" // Для работы с TLS/SSL
	"fmt"        // Для форматированного ввода/вывода
	"log"        // Для логирования ошибок
	"net/http"   // Для создания HTTP-сервера
)

// Добавляем middleware для CORS
func enableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Разрешаем запросы с любых доменов (*)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// Разрешенные HTTP-методы
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		// Разрешенные заголовки
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		// Для предварительных запросов OPTIONS сразу отвечаем OK
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// Передаем запрос следующему обработчику
		next.ServeHTTP(w, r)
	}
}

func main() {
	// Загрузка TLS-сертификата и приватного ключа
	cert, err := tls.LoadX509KeyPair("C:\\Program Files\\OpenSSL-Win64\\bin\\server.crt", "C:\\Program Files\\OpenSSL-Win64\\bin\\server.key")
	if err != nil {
		log.Fatal("Ошибка загрузки сертификата и ключа: ", err)
	}

	// Настраиваем TLS конфигурацию
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert}, // Наш сертификат
		MinVersion:   tls.VersionTLS12,        // Минимальная поддерживаемая версия TLS
	}

	// Создание HTTP-сервера с поддержкой TLS
	server := &http.Server{
		Addr:      ":8080",
		TLSConfig: tlsConfig, // Конфигурация TLS
		Handler:   nil,       // Используем стандартный роутер
	}

	http.HandleFunc("/register", enableCORS(registerHandler))
	http.HandleFunc("/login", enableCORS(loginHandler))
	http.HandleFunc("/users", enableCORS(usersHandler))
	http.HandleFunc("/chats", enableCORS(chatsHandler))
	http.HandleFunc("/messages", enableCORS(messagesHandler))
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/user/image", enableCORS(userImageHandler))
	http.HandleFunc("/add-reaction", enableCORS(addReactionHandler))
	http.HandleFunc("/get-reactions", enableCORS(getReactionsHandler))
	http.HandleFunc("/uploadFile", enableCORS(uploadFileHandler))
	http.HandleFunc("/user-status", enableCORS(getUserStatusHandler))
	http.HandleFunc("/user/profile", enableCORS(userProfileHandler))
	http.HandleFunc("/group-chats", enableCORS(createGroupChatHandler))
	http.HandleFunc("/all-users", enableCORS(allUsersHandler))
	http.HandleFunc("/forward-message", enableCORS(forwardMessage))
	http.HandleFunc("/reset_unread", resetUnreadHandler)
	http.HandleFunc("/group_participants_count", getGroupParticipantsCountHandler)
	http.HandleFunc("/group/image", enableCORS(groupImageHandler))
	// Запуск сервера
	fmt.Println("Server starting on :8080")
	// ListenAndServeTLS запускает HTTPS-сервер
	// Пустые строки - потому что сертификаты уже загружены в tlsConfig
	log.Fatal(server.ListenAndServeTLS("", ""))
}
