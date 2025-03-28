-- Подключение к базе данных 'mydatabase' под пользователем 'postgres' должно быть выполнено перед запуском

-- Удаление существующих таблиц в обратном порядке зависимостей, чтобы избежать ошибок
DROP TABLE IF EXISTS message_files;
DROP TABLE IF EXISTS message_reactions;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS participants;
DROP TABLE IF EXISTS group_chats;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS users;

-- Таблица пользователей
CREATE TABLE users (
    id SERIAL PRIMARY KEY,                -- Уникальный идентификатор пользователя
    username VARCHAR(255) UNIQUE NOT NULL,-- Уникальное имя пользователя
    password VARCHAR(255) NOT NULL,       -- Пароль (хэшированный)
    name VARCHAR(255),                    -- Имя пользователя (опционально)
    bio TEXT,                            -- Описание профиля (опционально)
    image BYTEA,                         -- Аватар пользователя в бинарном формате (опционально)
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP -- Дата создания аккаунта
);

-- Таблица чатов (как личных, так и групповых)
CREATE TABLE chats (
    id SERIAL PRIMARY KEY,                -- Уникальный идентификатор чата
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, -- Дата создания чата
    last_message_at TIMESTAMP,           -- Время последнего сообщения (обновляется триггером)
    is_group BOOLEAN NOT NULL DEFAULT FALSE -- Флаг, указывающий, является ли чат групповым
);

-- Таблица участников чатов (связь многие-ко-многим между users и chats)
CREATE TABLE participants (
    chat_id INT REFERENCES chats(id) ON DELETE CASCADE, -- ID чата, удаление чата каскадно удаляет записи
    user_id INT REFERENCES users(id) ON DELETE CASCADE, -- ID пользователя, удаление пользователя удаляет записи
    unread_count INT NOT NULL DEFAULT 0, -- Количество непрочитанных сообщений для пользователя в чате
    is_admin BOOLEAN NOT NULL DEFAULT FALSE, -- Флаг, указывающий, является ли участник администратором
    PRIMARY KEY (chat_id, user_id)       -- Составной первичный ключ
);

-- Таблица сообщений
CREATE TABLE messages (
    id SERIAL PRIMARY KEY,                -- Уникальный идентификатор сообщения
    chat_id INT NOT NULL REFERENCES chats(id) ON DELETE CASCADE, -- ID чата, к которому относится сообщение
    user_id INT REFERENCES users(id) ON DELETE SET NULL, -- ID отправителя (NULL, если пользователь удалён)
    content TEXT NOT NULL,               -- Текст сообщения
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, -- Время отправки сообщения
    is_system BOOLEAN NOT NULL DEFAULT FALSE, -- Флаг системного сообщения (например, "пользователь加入")
    parent_message_id INT REFERENCES messages(id) ON DELETE SET NULL, -- ID родительского сообщения (для ответов)
    is_forwarded BOOLEAN NOT NULL DEFAULT FALSE, -- Флаг пересланного сообщения
    original_sender_id INT REFERENCES users(id) ON DELETE SET NULL, -- ID исходного отправителя (для пересылки)
    original_chat_id INT REFERENCES chats(id) ON DELETE SET NULL -- ID исходного чата (для пересылки)
);

-- Таблица реакций на сообщения
CREATE TABLE message_reactions (
    id SERIAL PRIMARY KEY,                -- Уникальный идентификатор реакции
    message_id INT NOT NULL REFERENCES messages(id) ON DELETE CASCADE, -- ID сообщения, на которое реакция
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- ID пользователя, поставившего реакцию
    reaction VARCHAR(50) NOT NULL,       -- Текст реакции (например, "👍" или "😂")
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, -- Время добавления реакции
    UNIQUE (message_id, user_id)         -- Ограничение: один пользователь — одна реакция на сообщение
);

-- Таблица файлов, прикреплённых к сообщениям
CREATE TABLE message_files (
    id SERIAL PRIMARY KEY,                -- Уникальный идентификатор файла
    message_id INT NOT NULL REFERENCES messages(id) ON DELETE CASCADE, -- ID сообщения, к которому файл прикреплён
    file_name VARCHAR(255) NOT NULL,     -- Имя файла
    file_data BYTEA NOT NULL,            -- Бинарные данные файла
    uploaded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP -- Время загрузки файла
);

-- Таблица групповых чатов (дополнительная информация для чатов с is_group = TRUE)
CREATE TABLE group_chats (
    id SERIAL PRIMARY KEY,                -- Уникальный идентификатор записи группового чата
    chat_id INT REFERENCES chats(id) ON DELETE CASCADE, -- Связь с таблицей chats
    name VARCHAR(255) NOT NULL,          -- Название группы
    description TEXT,                    -- Описание группы (опционально)
    created_by INT REFERENCES users(id) ON DELETE SET NULL, -- ID создателя группы
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, -- Дата создания группы
    image BYTEA                          -- Изображение группы (опционально)
);

-- Функция для обновления времени последнего сообщения в чате
CREATE OR REPLACE FUNCTION update_chat_last_message()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE chats SET last_message_at = NEW.created_at WHERE id = NEW.chat_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Триггер для автоматического обновления last_message_at при добавлении нового сообщения
CREATE TRIGGER trigger_update_chat_last_message
AFTER INSERT ON messages
FOR EACH ROW
EXECUTE FUNCTION update_chat_last_message();

-- Индексы для оптимизации запросов
CREATE INDEX idx_messages_parent ON messages(parent_message_id); -- Для быстрого поиска ответов на сообщения
CREATE INDEX idx_messages_original ON messages(original_chat_id, original_sender_id); -- Для поиска пересланных сообщений