# Серверная часть проекта "Мессенджер"
## [ скриншоты работы приложения в клиентской части (ссылка) ](https://github.com/yehoto/MessengerClient)
## Функциональность (WIP - Work in Progress)
Данный проект находится в разработке. Ниже представлен список реализованных и планируемых функций. Обратите внимание, что текущий функционал может быть неполным или нестабильным.
## Регистрация
- Валидация обязательных полей
- Загрузка аватарки (поддержка JPEG/PNG)
- Хэширование паролей (bcrypt)
## Аутентификация
- Логин с проверкой учётных данных
- Межплатформенные сессии
## Управление чатами
- Создание личных/групповых чатов
- Добавление/удаление участников
- Назначение администраторов групп
- Просмотр списка чатов с сортировкой по активности
- Системные уведомления
- Поиск по чатам
- Сосздение,удаление чата
- Редактирование информации о чате
## Сообщения
- Отправка/получение сообщений в ресльном времени (WebSocket)
- Пересылка сообщений между чатами
- Реакции смайликами
- Прикрепление файлов
- Просмотр пересланных сообщений с указанием источника
- Автопоиск источника пересланного сообщения (прокрутка к нему и подсветка)
- Поиск по истории сообщений с подсветкой результатов и автопрокруткой
- Системные уведомления
- Скачивание вложений
- Кеширование
- Шифрование
## Статусы и активность
- Индикаторы онлайн/оффлайн
- Время последней активности
- Счётчики непрочитанных сообщений
- Отображение времени доставки/прочтения/изменения сообщения
## Профили пользователей
- Просмотри профиля
- История регистрации
- Редактирование/удаление своего профиля
- Поиск пользователей по username
## Безопасность
- CORS-политики
- Защита от XSS/CSRF
- Валидация входных данных
- Логирование операций
## База данных
- Оптимизированние SQL-запросы
- Триггеры для обновления времени последнего сообщения
- Индексы для ускорения поиска
- Каскадное удаление данных
## Кроссплатформенность
- Поддержка Web, Android, iOS, Windows, MacOS
- Адаптивный дизайн
- Нативные компоненты для каждой платформы
## Другое
- Многопоточная обработка запросов на сервере
- Локализация (EN/RU)
- Управление уведомлениями (звук, вибрация)
- Автоподгрузка истории при скролле
- Анимации интерфейса (появление сообщений, реакции)
- Система кэширования изображений на клиенте
- Обработка ошибок
- Автоматическое переподключение WebSocket
