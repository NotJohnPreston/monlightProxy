package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type RTSConnection struct {
	ID            string  `json:"id"`
	Created       string  `json:"created"`
	RemoteAddr    string  `json:"remoteAddr"`
	BytesReceived int64   `json:"bytesReceived"`
	BytesSent     int64   `json:"bytesSent"`
	Session       *string `json:"session"` // nullable согласно OpenAPI
	Tunnel        string  `json:"tunnel"`
}

type RTSConnectionsResponse struct {
	PageCount int             `json:"pageCount"`
	ItemCount int             `json:"itemCount"`
	Items     []RTSConnection `json:"items"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

var (
	baseURL             string
	authUser            string
	authPass            string
	authenticatedClient *http.Client
	clientMutex         sync.RWMutex
	lastAuthTime        time.Time
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Println("Предупреждение: .env файл не найден")
	}

	baseURL = os.Getenv("BASE_URL")
	authUser = os.Getenv("AUTH_USER")
	authPass = os.Getenv("AUTH_PASS")

	if baseURL == "" || authUser == "" || authPass == "" {
		log.Fatal("Ошибка: BASE_URL, AUTH_USER и AUTH_PASS должны быть установлены в .env файле")
	}
}

func main() {
	// Проверяем, включен ли mock режим
	mockMode := os.Getenv("MOCK_MODE")
	if mockMode == "true" || mockMode == "1" {
		log.Println("⚠️  ВНИМАНИЕ: Запущен в MOCK режиме - возвращаются тестовые данные!")
	} else {
		// Проверяем доступность API
		log.Println("🔍 Проверка подключения к API...")
		if err := testAPIConnection(); err != nil {
			log.Printf("⚠️  ПРЕДУПРЕЖДЕНИЕ: Не удалось подключиться к API: %v", err)
			log.Println("💡 Убедитесь что:")
			log.Println("   1. Сервер запущен")
			log.Println("   2. BASE_URL правильный")
			log.Println("   3. Credentials верные")
			log.Println("   Или включите MOCK_MODE=true для тестирования")
		} else {
			log.Println("✅ API доступен")
		}
	}

	http.HandleFunc("/api/connections", getConnectionsHandler)
	http.HandleFunc("/api/debug", debugHandler)
	http.HandleFunc("/health", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("\n🚀 Сервер запущен на порту %s", port)
	log.Printf("📍 BASE_URL: %s", baseURL)
	log.Printf("👤 AUTH_USER: %s", authUser)
	log.Printf("\n📚 Доступные эндпоинты:")
	log.Printf("   GET http://localhost:%s/api/connections?page=1&itemsPerPage=10", port)
	log.Printf("   GET http://localhost:%s/api/debug - отладочная информация", port)
	log.Printf("   GET http://localhost:%s/health - health check", port)

	if mockMode == "true" || mockMode == "1" {
		log.Printf("\n🔧 Для отключения mock режима удалите MOCK_MODE из .env")
	} else {
		log.Printf("\n💡 Если API не работает, добавьте MOCK_MODE=true в .env для тестовых данных")
	}
	log.Println()

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

// Проверка доступности API
func testAPIConnection() error {
	client := &http.Client{Timeout: 5 * time.Second}

	// Пробуем получить список подключений
	testURL := baseURL + "/api/v3/rtspconns/list?page=0&itemsPerPage=1"
	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return fmt.Errorf("ошибка создания запроса: %v", err)
	}

	req.SetBasicAuth(authUser, authPass)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ошибка подключения: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 {
		return fmt.Errorf("ошибка аутентификации (401) - проверьте AUTH_USER и AUTH_PASS")
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("неожиданный статус %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	// Проверяем, что ответ - JSON
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("получен не JSON ответ (Content-Type: %s)", resp.Header.Get("Content-Type"))
	}

	return nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Получение или создание аутентифицированного HTTP клиента
func getAuthenticatedClient() (*http.Client, error) {
	clientMutex.RLock()
	if authenticatedClient != nil && time.Since(lastAuthTime) < 50*time.Minute {
		client := authenticatedClient
		clientMutex.RUnlock()
		return client, nil
	}
	clientMutex.RUnlock()

	clientMutex.Lock()
	defer clientMutex.Unlock()

	// Двойная проверка
	if authenticatedClient != nil && time.Since(lastAuthTime) < 50*time.Minute {
		return authenticatedClient, nil
	}

	log.Println("Создание HTTP клиента...")

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания cookie jar: %v", err)
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}

	authenticatedClient = client
	lastAuthTime = time.Now()

	return client, nil
}

func debugHandler(w http.ResponseWriter, r *http.Request) {
	client, err := getAuthenticatedClient()
	if err != nil {
		sendError(w, http.StatusInternalServerError, fmt.Sprintf("Ошибка создания клиента: %v", err))
		return
	}

	results := make(map[string]interface{})

	// Добавляем информацию о конфигурации
	results["config"] = map[string]string{
		"BASE_URL":  baseURL,
		"AUTH_USER": authUser,
		"MOCK_MODE": os.Getenv("MOCK_MODE"),
	}

	// Проверяем разные варианты эндпоинтов
	testCases := []struct {
		name        string
		url         string
		description string
	}{
		{
			name:        "GET /api/v3/rtspconns/list",
			url:         baseURL + "/api/v3/rtspconns/list?page=0&itemsPerPage=10",
			description: "Список RTSP подключений",
		},
		{
			name:        "GET /api/v3/webrtcsessions/list",
			url:         baseURL + "/api/v3/webrtcsessions/list?page=0&itemsPerPage=10",
			description: "Список WebRTC сессий",
		},
		{
			name:        "GET /api/v3/rtspsessions/list",
			url:         baseURL + "/api/v3/rtspsessions/list?page=0&itemsPerPage=10",
			description: "Список RTSP сессий",
		},
	}

	for _, tc := range testCases {
		log.Printf("🧪 Тестирование: %s", tc.name)

		req, err := http.NewRequest("GET", tc.url, nil)
		if err != nil {
			results[tc.name] = map[string]interface{}{
				"error":       err.Error(),
				"url":         tc.url,
				"description": tc.description,
			}
			continue
		}

		req.SetBasicAuth(authUser, authPass)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			results[tc.name] = map[string]interface{}{
				"error":       err.Error(),
				"url":         tc.url,
				"description": tc.description,
			}
			log.Printf("   ❌ Ошибка: %v", err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		contentType := resp.Header.Get("Content-Type")
		isJSON := strings.Contains(contentType, "application/json") || (len(body) > 0 && body[0] == '{')

		result := map[string]interface{}{
			"url":         tc.url,
			"description": tc.description,
			"status":      resp.StatusCode,
			"contentType": contentType,
			"isJSON":      isJSON,
			"bodyLength":  len(body),
			"bodyPreview": string(body[:min(len(body), 300)]),
		}

		// Если нашли JSON, выделяем это
		if isJSON && resp.StatusCode == 200 {
			result["✅ SUCCESS"] = true
			log.Printf("   ✅ Успех! Статус: %d", resp.StatusCode)
		} else if resp.StatusCode == 401 {
			result["⚠️ WARNING"] = "Ошибка аутентификации"
			log.Printf("   ⚠️  401 Unauthorized - проверьте credentials")
		} else if !isJSON {
			result["⚠️ WARNING"] = "Получен не JSON ответ"
			log.Printf("   ⚠️  Не JSON: %s", contentType)
		} else {
			log.Printf("   ❌ Статус: %d", resp.StatusCode)
		}

		results[tc.name] = result
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(results)
}

func getConnectionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendError(w, http.StatusMethodNotAllowed, "Разрешен только GET метод")
		return
	}

	// Получение параметров
	pageStr := r.URL.Query().Get("page")
	itemsPerPageStr := r.URL.Query().Get("itemsPerPage")

	// MediaMTX использует нумерацию страниц с 0, но для удобства пользователей мы принимаем с 1
	page := 0
	itemsPerPage := 100 // По умолчанию в MediaMTX

	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p - 1 // Конвертируем в 0-based индекс для MediaMTX
		}
	}

	if itemsPerPageStr != "" {
		if i, err := strconv.Atoi(itemsPerPageStr); err == nil && i > 0 {
			itemsPerPage = i
		}
	}

	// Проверяем mock режим
	mockMode := os.Getenv("MOCK_MODE")
	if mockMode == "true" || mockMode == "1" {
		log.Printf("📦 Mock режим: возвращаем тестовые данные (page=%d, itemsPerPage=%d)", page+1, itemsPerPage)
		mockResponse := generateMockData(page+1, itemsPerPage) // Конвертируем обратно для mock
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(mockResponse)
		return
	}

	// Реальный запрос к API
	client, err := getAuthenticatedClient()
	if err != nil {
		sendError(w, http.StatusInternalServerError, fmt.Sprintf("Ошибка аутентификации: %v. Попробуйте включить MOCK_MODE=true в .env", err))
		return
	}

	apiURL := fmt.Sprintf("%s/api/v3/rtspconns/list?page=%d&itemsPerPage=%d", baseURL, page, itemsPerPage)
	log.Printf("🔍 Запрос к API:")
	log.Printf("   URL: %s", apiURL)
	log.Printf("   Page: %d, ItemsPerPage: %d", page, itemsPerPage)
	log.Printf("   Auth User: %s", authUser)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		sendError(w, http.StatusInternalServerError, "Ошибка создания запроса")
		return
	}

	// MediaMTX использует Basic Auth для internal authentication
	// Добавляем credentials к каждому запросу
	req.SetBasicAuth(authUser, authPass)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		sendError(w, http.StatusBadGateway, fmt.Sprintf("Ошибка запроса: %v. Попробуйте MOCK_MODE=true", err))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		sendError(w, http.StatusInternalServerError, "Ошибка чтения ответа")
		return
	}

	log.Printf("📊 Ответ от MediaMTX:")
	log.Printf("   Статус: %d", resp.StatusCode)
	log.Printf("   Content-Type: %s", resp.Header.Get("Content-Type"))
	log.Printf("   Размер ответа: %d байт", len(body))

	if len(body) > 0 {
		preview := string(body[:min(len(body), 500)])
		log.Printf("   Тело ответа (первые 500 символов):\n%s", preview)
	}

	// Если получили HTML, пробуем сбросить сессию
	if strings.Contains(string(body), "<!DOCTYPE html>") {
		clientMutex.Lock()
		authenticatedClient = nil
		clientMutex.Unlock()
		sendError(w, http.StatusUnauthorized, "API возвращает HTML вместо JSON. Возможно:\n1. Неверные credentials\n2. API недоступен\n3. Требуется другой метод аутентификации\n\nПопробуйте включить MOCK_MODE=true в .env для тестовых данных")
		return
	}

	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(ErrorResponse{
			Error:   fmt.Sprintf("Статус %d", resp.StatusCode),
			Message: string(body[:min(len(body), 200)]),
		})
		return
	}

	var connResponse RTSConnectionsResponse
	if err := json.Unmarshal(body, &connResponse); err != nil {
		sendError(w, http.StatusInternalServerError, fmt.Sprintf("Ошибка парсинга JSON: %v", err))
		log.Printf("Не удалось распарсить: %s", string(body[:min(len(body), 1000)]))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(connResponse)
}

// Генерация mock данных для тестирования
func generateMockData(page, itemsPerPage int) RTSConnectionsResponse {
	totalItems := 47 // Общее количество элементов
	pageCount := (totalItems + itemsPerPage - 1) / itemsPerPage

	// Рассчитываем, какие элементы показывать на этой странице
	startIdx := (page - 1) * itemsPerPage
	endIdx := startIdx + itemsPerPage
	if endIdx > totalItems {
		endIdx = totalItems
	}

	items := []RTSConnection{}

	for i := startIdx; i < endIdx; i++ {
		sessionID := fmt.Sprintf("session_%03d", i+1)
		conn := RTSConnection{
			ID:            fmt.Sprintf("conn_%03d", i+1),
			Created:       time.Now().Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
			RemoteAddr:    fmt.Sprintf("192.168.%d.%d:%d", (i/100)%256, i%256, 50000+i),
			BytesReceived: int64((i + 1) * 1024 * 1024),
			BytesSent:     int64((i + 1) * 2048 * 1024),
			Session:       &sessionID, // nullable поле
			Tunnel:        fmt.Sprintf("tunnel_%d", (i%5)+1),
		}
		items = append(items, conn)
	}

	return RTSConnectionsResponse{
		PageCount: pageCount,
		ItemCount: totalItems,
		Items:     items,
	}
}

func sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{
		Error:   http.StatusText(status),
		Message: message,
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
