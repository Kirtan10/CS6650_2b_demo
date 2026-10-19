// product-service.go
//
// CS6650 Homework 5: Product API (GET + POST) from api.yaml, built with Gin.
//
// Request path:  Gin router -> observe() middleware (metrics + JSON log)
//                -> recovery (turns panics into 500) -> fault injection (optional)
//                -> handler -> ProductStore (map + RWMutex) -> JSON response
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---------------------------------------------------------------------------
// Schemas (components/schemas in api.yaml)
// ---------------------------------------------------------------------------

// Product is what we store and what GET returns.
type Product struct {
	ProductID    int    `json:"product_id"`
	SKU          string `json:"sku"`
	Manufacturer string `json:"manufacturer"`
	CategoryID   int    `json:"category_id"`
	Weight       int    `json:"weight"`
	SomeOtherID  int    `json:"some_other_id"`
}

// productInput is the POST body. Pointers let us tell a *missing* field
// (nil) apart from a field sent as 0 or "" - both must be rejected, but
// for different reasons, and the spec marks every field as required.
type productInput struct {
	ProductID    *int    `json:"product_id"`
	SKU          *string `json:"sku"`
	Manufacturer *string `json:"manufacturer"`
	CategoryID   *int    `json:"category_id"`
	Weight       *int    `json:"weight"`
	SomeOtherID  *int    `json:"some_other_id"`
}

// ErrorResponse matches the Error schema: error + message required, details optional.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// ---------------------------------------------------------------------------
// In-memory store: map for O(1) lookup by ID, RWMutex for safe concurrency.
// Many GETs can read at once (RLock); a POST takes the exclusive Lock.
// Without the mutex, concurrent map writes crash the Go runtime
// ("fatal error: concurrent map writes") - the race we saw in HW3.
// ---------------------------------------------------------------------------

type ProductStore struct {
	mu       sync.RWMutex
	products map[int]Product
}

func NewProductStore() *ProductStore {
	return &ProductStore{products: make(map[int]Product)}
}

func (s *ProductStore) Get(id int) (Product, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.products[id]
	return p, ok
}

// UpdateDetails replaces the details of an existing product.
// Returns false if the product does not exist (-> 404).
func (s *ProductStore) UpdateDetails(p Product) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.products[p.ProductID]; !ok {
		return false
	}
	s.products[p.ProductID] = p
	return true
}

// Seed loads the starting catalog (products 1..n) so the store has items
// to sell, and so load tests have something to GET and update.
func (s *ProductStore) Seed(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 1; i <= n; i++ {
		s.products[i] = Product{
			ProductID:    i,
			SKU:          fmt.Sprintf("SKU-%06d", i),
			Manufacturer: "Acme Corporation",
			CategoryID:   i%50 + 1,
			Weight:       100 + i%900,
			SomeOtherID:  i,
		}
	}
}

func (s *ProductStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.products)
}

// ---------------------------------------------------------------------------
// Observability: RED metrics (Prometheus) + one JSON log line per request
// ---------------------------------------------------------------------------

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests (Rate).",
	}, []string{"method", "route", "status"})

	errorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_errors_total",
		Help: "HTTP requests that returned a non-2xx status (Errors).",
	}, []string{"method", "route", "status"})

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds",
		Help: "HTTP request latency in seconds (Duration).",
		// Our handlers are sub-millisecond, so the default buckets (5ms+) are too coarse.
		Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"method", "route"})
)

// observe records metrics and writes a structured log line for every request.
// It is registered first, so it wraps everything else and sees the final status,
// including 500s produced by the recovery middleware.
func observe(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		// Use the route template ("/products/:productId"), not the raw path,
		// so metrics don't get one time series per product ID.
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		if route == "/metrics" || route == "/health" {
			return // don't let scrapes/health checks pollute the RED data
		}

		status := c.Writer.Status()
		elapsed := time.Since(start)
		code := strconv.Itoa(status)
		method := c.Request.Method

		requestsTotal.WithLabelValues(method, route, code).Inc()
		if status < 200 || status > 299 {
			errorsTotal.WithLabelValues(method, route, code).Inc()
		}
		requestDuration.WithLabelValues(method, route).Observe(elapsed.Seconds())

		logger.Info("request",
			"method", method,
			"endpoint", route,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", float64(elapsed.Microseconds())/1000.0,
			"bytes", c.Writer.Size(),
			"client_ip", c.ClientIP(),
		)
	}
}

// faultInjection lets us demonstrate the 500 response on demand.
// Only active when ENABLE_FAULT_INJECTION=true, so it is off in production.
func faultInjection(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if enabled && c.GetHeader("X-Fault-Injection") == "500" {
			panic("injected fault for testing the 500 response")
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func badRequest(c *gin.Context, msg, details string) {
	c.JSON(http.StatusBadRequest, ErrorResponse{Error: "INVALID_INPUT", Message: msg, Details: details})
}

func notFound(c *gin.Context, id int) {
	c.JSON(http.StatusNotFound, ErrorResponse{
		Error:   "NOT_FOUND",
		Message: "Product not found",
		Details: fmt.Sprintf("no product with product_id %d", id),
	})
}

// parseProductID checks the {productId} path parameter against the spec:
// type integer, format int32, minimum 1. ParseInt with bitSize 32 rejects
// anything outside the int32 range.
func parseProductID(raw string) (int, bool) {
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || id < 1 {
		return 0, false
	}
	return int(id), true
}

// GET /products/{productId}
func getProduct(store *ProductStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		// api.yaml lists only 200, 404 and 500 for this GET, so an ID that
		// isn't a positive int32 gets 404: no product can have that ID.
		raw := c.Param("productId")
		id, ok := parseProductID(raw)
		if !ok {
			c.JSON(http.StatusNotFound, ErrorResponse{
				Error:   "NOT_FOUND",
				Message: "Product not found",
				Details: fmt.Sprintf("productId must be a positive 32-bit integer, got %q", raw),
			})
			return
		}
		p, found := store.Get(id)
		if !found {
			notFound(c, id)
			return
		}
		c.JSON(http.StatusOK, p)
	}
}

// POST /products/{productId}/details
func addProductDetails(store *ProductStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		// POST does list 400, so a bad path ID is invalid input here.
		raw := c.Param("productId")
		id, ok := parseProductID(raw)
		if !ok {
			badRequest(c, "Invalid product ID", fmt.Sprintf("productId must be a positive 32-bit integer, got %q", raw))
			return
		}

		// Decode the body ourselves (instead of ShouldBindJSON) so we control
		// the error messages and reject wrong types, e.g. "weight": "heavy".
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		dec := json.NewDecoder(c.Request.Body)
		var in productInput
		if err := dec.Decode(&in); err != nil {
			if errors.Is(err, io.EOF) {
				badRequest(c, "Invalid input data", "request body is required")
			} else {
				badRequest(c, "Invalid input data", "malformed JSON: "+err.Error())
			}
			return
		}
		if dec.More() {
			badRequest(c, "Invalid input data", "request body must contain a single JSON object")
			return
		}

		if problems := validateProduct(in); len(problems) > 0 {
			badRequest(c, "Invalid input data", strings.Join(problems, "; "))
			return
		}
		if *in.ProductID != id {
			badRequest(c, "Invalid input data",
				fmt.Sprintf("product_id in body (%d) does not match productId in path (%d)", *in.ProductID, id))
			return
		}

		p := Product{
			ProductID:    *in.ProductID,
			SKU:          *in.SKU,
			Manufacturer: *in.Manufacturer,
			CategoryID:   *in.CategoryID,
			Weight:       *in.Weight,
			SomeOtherID:  *in.SomeOtherID,
		}
		if !store.UpdateDetails(p) {
			notFound(c, id)
			return
		}
		c.Status(http.StatusNoContent) // 204: success, no body
	}
}

// validateProduct enforces the Product schema's required fields and constraints.
func validateProduct(in productInput) []string {
	var problems []string
	checkInt := func(name string, v *int, min int) {
		if v == nil {
			problems = append(problems, name+" is required")
		} else if *v < min {
			problems = append(problems, fmt.Sprintf("%s must be >= %d", name, min))
		} else if *v > math.MaxInt32 { // format: int32
			problems = append(problems, fmt.Sprintf("%s must fit in a 32-bit integer", name))
		}
	}
	checkStr := func(name string, v *string, maxLen int) {
		if v == nil {
			problems = append(problems, name+" is required")
			return
		}
		n := utf8.RuneCountInString(*v)
		if n < 1 || n > maxLen {
			problems = append(problems, fmt.Sprintf("%s must be 1-%d characters", name, maxLen))
		}
	}
	checkInt("product_id", in.ProductID, 1)
	checkStr("sku", in.SKU, 100)
	checkStr("manufacturer", in.Manufacturer, 200)
	checkInt("category_id", in.CategoryID, 1)
	checkInt("weight", in.Weight, 0)
	checkInt("some_other_id", in.SomeOtherID, 1)
	return problems
}

// ---------------------------------------------------------------------------
// Router + main
// ---------------------------------------------------------------------------

func newRouter(store *ProductStore, logger *slog.Logger, faults bool) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)

	r.Use(
		observe(logger),
		gin.CustomRecovery(func(c *gin.Context, err any) {
			logger.Error("panic recovered", "error", fmt.Sprint(err), "path", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorResponse{
				Error:   "INTERNAL_ERROR",
				Message: "Internal server error",
			})
		}),
		faultInjection(faults),
	)

	r.GET("/products/:productId", getProduct(store))
	r.POST("/products/:productId/details", addProductDetails(store))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "products": store.Len()})
	})
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, ErrorResponse{Error: "NOT_FOUND", Message: "Route not found"})
	})
	return r
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	store := NewProductStore()
	store.Seed(envInt("SEED_PRODUCTS", 1000))

	faults := os.Getenv("ENABLE_FAULT_INJECTION") == "true"
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // must match container_port in terraform/variables.tf
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           newRouter(store, logger, faults),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("product service starting", "port", port, "seeded", store.Len(), "fault_injection", faults)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
