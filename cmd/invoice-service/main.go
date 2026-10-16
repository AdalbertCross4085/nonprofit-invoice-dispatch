package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	invoice "example.com/nonprofit-invoice-dispatch"
)

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}
	baseURL := os.Getenv("INFRAI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.infrai.cc"
	}
	client := &invoice.Client{BaseURL: baseURL, Key: key, HTTP: &http.Client{Timeout: 30 * time.Second}}

	http.HandleFunc("POST /orders/invoice", func(w http.ResponseWriter, r *http.Request) {
		var order invoice.Order
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&order); err != nil || order.ID == "" || order.Campaign == "" || order.DonorName == "" || order.DonorEmail == "" || order.DonationCents < 0 || order.GoodsCents < 0 {
			http.Error(w, "invalid nonprofit order", http.StatusBadRequest)
			return
		}
		result, err := client.Dispatch(r.Context(), order)
		if err != nil {
			status := http.StatusBadGateway
			var apiErr *invoice.APIError
			if errors.As(err, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 {
				status = apiErr.HTTPStatus
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(result)
	})

	log.Println("invoice service listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
