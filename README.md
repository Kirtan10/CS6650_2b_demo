# CS6650 Homework 5: Product API on ECS with Terraform

Kirtan Patel · Building Scalable Distributed Systems

The first piece of a simple online store: a Go/Gin **Product API** that implements the Product endpoints of `api.yaml`, packaged with Docker, deployed to AWS ECS (Fargate) with Terraform, load tested with Locust, and observed with Prometheus, Grafana and structured JSON logs.

## Where everything lives

| Path | What it is |
|---|---|
| `src/product-service.go` | The server: handlers, validation, in-memory store, metrics, logging |
| `src/go.mod`, `src/go.sum` | Go module and pinned dependencies |
| `src/Dockerfile` | Multi-stage build: compiles a static binary, ships it on Alpine |
| `terraform/` | Infrastructure as code (forked from the course starter repo): ECR, ECS, networking, logs |
| `scripts/test_all_codes.sh` | One curl per response code, for local or deployed servers |
| `scripts/get_task_ip.sh` | Finds the public IP of the running ECS task |
| `locust/` | Locustfile (HttpUser/FastHttpUser switch, stress mode) and master/worker compose file |
| `observability/` | Local Prometheus + Grafana stack with a pre-built RED dashboard |
| `screenshots/` | Response-code, Locust and Grafana screenshots |
| `docs/INTERVIEW_PREP.md` | Answers for the mock interview questions |

## The API

| Endpoint | Purpose | Responses |
|---|---|---|
| `GET /products/{productId}` | Get one product | 200 product JSON · 404 not found · 500 server error |
| `POST /products/{productId}/details` | Add/replace a product's details | 204 no body · 400 invalid input · 404 not found · 500 server error |
| `GET /health` | Liveness check (not in the spec) | 200 |
| `GET /metrics` | Prometheus metrics (not in the spec) | 200 |

A `Product` has six required fields: `product_id` (int32 ≥ 1), `sku` (1–100 chars), `manufacturer` (1–200 chars), `category_id` (int32 ≥ 1), `weight` (int32 ≥ 0, grams) and `some_other_id` (int32 ≥ 1). Every integer is `format: int32` in the spec, so values above 2,147,483,647 are rejected too. Errors use the spec's `Error` schema: `{"error": "...", "message": "...", "details": "..."}`.

**Design decisions worth knowing:**

- **POST adds or updates details of an existing product.** The spec describes it as "add or update detailed information for a specific product" and lists 404, so the product must already exist. The server starts with a seeded catalog (products `1..SEED_PRODUCTS`, default 1000) and POST sets the details of one of them.
- **An invalid ID on GET returns 404, not 400.** The spec lists only 200, 404 and 500 for GET, so `/products/abc` or `/products/0` gets 404: no product can have that ID. POST does list 400, so there an invalid path ID is a 400.
- **No authentication.** The spec declares API-key and Bearer security schemes globally, but defines no 401 response for these endpoints, so auth is out of scope for this assignment.
- **The body's `product_id` must match the path's `productId`**, otherwise 400. Silently trusting either one would let a client write product 7's data into product 2.
- **500 is triggered with fault injection.** A correct server should never return 500 on its own, so to demonstrate it, the server panics when `ENABLE_FAULT_INJECTION=true` *and* the request has the header `X-Fault-Injection: 500`. Gin's recovery middleware turns the panic into a proper 500 `Error` response. This is turned on only in local compose, never on ECS.

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Must match `container_port` in `terraform/variables.tf` |
| `SEED_PRODUCTS` | `1000` | Size of the starting catalog |
| `ENABLE_FAULT_INJECTION` | off | `true` enables the `X-Fault-Injection: 500` header |
| `GIN_MODE` | `release` (in Docker) | Gin's log/debug mode |

## Run it locally

**Prerequisites:** Go 1.22+, Docker Desktop.

```bash
cd src
go mod tidy                                   # creates go.sum on first run
ENABLE_FAULT_INJECTION=true go run .          # server on :8080
```

**In Docker** (`--platform linux/amd64` is only needed when building on Apple Silicon for AWS; for local runs you can leave it off):

```bash
cd src
docker build -t product-service .
docker run --rm -p 8080:8080 -e ENABLE_FAULT_INJECTION=true product-service
```

Then, from the repo root in another terminal:

```bash
./scripts/test_all_codes.sh http://localhost:8080
```

## Every response code, with examples

Screenshots of each case are in `screenshots/responses/` (local `go run`, Docker, and the deployed ECS server).

**GET 200: product exists**
```bash
curl -i http://localhost:8080/products/1
# HTTP/1.1 200 OK
# {"product_id":1,"sku":"SKU-000001","manufacturer":"Acme Corporation","category_id":2,"weight":101,"some_other_id":1}
```

**GET 404: product not found**
```bash
curl -i http://localhost:8080/products/999999
# HTTP/1.1 404 Not Found
# {"error":"NOT_FOUND","message":"Product not found","details":"no product with product_id 999999"}

curl -i http://localhost:8080/products/abc      # not a valid ID, so also 404 (GET has no 400 in the spec)
# {"error":"NOT_FOUND","message":"Product not found","details":"productId must be a positive 32-bit integer, got \"abc\""}
```

**GET 500: server error (fault injection)**
```bash
curl -i -H 'X-Fault-Injection: 500' http://localhost:8080/products/1
# HTTP/1.1 500 Internal Server Error
# {"error":"INTERNAL_ERROR","message":"Internal server error"}
```

**POST 204: details added**
```bash
curl -i -X POST http://localhost:8080/products/1/details \
  -H 'Content-Type: application/json' \
  -d '{"product_id":1,"sku":"ABC-123-XYZ","manufacturer":"Acme Corporation","category_id":456,"weight":1250,"some_other_id":789}'
# HTTP/1.1 204 No Content
```

**POST 400: invalid input** (any of these)
```bash
# malformed JSON
curl -i -X POST http://localhost:8080/products/1/details -H 'Content-Type: application/json' -d '{"product_id":1,'
# invalid path ID
curl -i -X POST http://localhost:8080/products/abc/details -H 'Content-Type: application/json' -d '{}'
# missing fields
curl -i -X POST http://localhost:8080/products/1/details -H 'Content-Type: application/json' -d '{"product_id":1,"sku":"X"}'
# HTTP/1.1 400 Bad Request
# {"error":"INVALID_INPUT","message":"Invalid input data","details":"manufacturer is required; category_id is required; weight is required; some_other_id is required"}
```

**POST 404: product not found**
```bash
curl -i -X POST http://localhost:8080/products/999999/details -H 'Content-Type: application/json' \
  -d '{"product_id":999999,"sku":"ABC","manufacturer":"Acme","category_id":1,"weight":1,"some_other_id":1}'
# HTTP/1.1 404 Not Found
```

**POST 500: server error (fault injection)**
```bash
curl -i -X POST -H 'X-Fault-Injection: 500' http://localhost:8080/products/1/details \
  -H 'Content-Type: application/json' -d '{}'
# HTTP/1.1 500 Internal Server Error
```

## Deploy to AWS on any machine

**Prerequisites:** AWS CLI v2, Terraform ≥ 1.5, Docker Desktop running, and AWS credentials. On AWS Academy Learner Lab, start the lab, open **AWS Details → AWS CLI: Show**, and paste the block into `~/.aws/credentials` under `[default]` (the credentials expire when the lab session ends, so repeat this each session). Then check them:

```bash
aws configure set region us-west-2
aws sts get-caller-identity          # should print your account ID
```

**Deploy:**

```bash
git clone <this repo> && cd <repo>/terraform
terraform init                       # downloads the AWS + Docker providers
terraform plan                       # preview what will be created
terraform apply                      # type "yes"; builds the image, pushes to ECR, starts ECS
terraform output                     # cluster name, service name, ECR URL
```

Terraform builds `src/Dockerfile`, pushes the image to ECR, and creates the ECS cluster, task definition, service, security group (port 8080 open) and CloudWatch log group.

**Find the server and test it:**

```bash
IP=$(../scripts/get_task_ip.sh <cluster_name> <service_name>)   # names from terraform output
curl http://$IP:8080/health
../scripts/test_all_codes.sh http://$IP:8080
```

The task can take 1–2 minutes to reach RUNNING after `apply` finishes. On ECS, fault injection is off, so the two 500 checks return normal responses there; 500 is demonstrated locally.

**View the request logs** in CloudWatch: ECS → cluster → service → Logs tab, or `aws logs tail <log_group_name> --follow`.

**Tear down** when you're done, so the lab budget isn't spent on an idle service:

```bash
terraform destroy
```

**Building on Apple Silicon:** Fargate runs `linux/amd64`. If the image is built by Terraform's Docker provider, the `build` block in `terraform/main.tf` needs `platform = "linux/amd64"`. Without it, the task starts and immediately exits with `exec format error`.

### What I changed from the starter repo

1. Deleted `src/main.go` (the albums server) and added `product-service.go`, `go.mod` and `go.sum`.
2. Replaced `src/Dockerfile` with the multi-stage Product API Dockerfile, keeping the name and location so Terraform's build context (`../src`) still finds it.
3. Kept the server on port 8080 to match `container_port` in `terraform/variables.tf`.
4. Changed the service/repository names in `terraform/variables.tf` from the album server's to `product-service`. *(Fill in the exact variables you renamed.)*
5. Added `platform = "linux/amd64"` to the Docker image build. *(If your starter version already had it, say so here.)*

## Part IV: Load and stress testing

```bash
cd locust
CLIENT=fast WAIT=normal docker compose up --scale worker=4
# open http://localhost:8089 and set Host to http://<TASK_PUBLIC_IP>:8080
```

| Knob | Options | Use |
|---|---|---|
| `CLIENT` | `http` / `fast` | HttpUser (python-requests) vs FastHttpUser (geventhttpclient) |
| `WAIT` | `normal` / `none` | 0.1–0.5 s think time vs no think time (stress) |
| `STEP_SHAPE` | unset / `1` | Automatic ramp: +100 users every 60 s up to 1000 |
| `--scale worker=N` | 1, 2, 4… | More load generators, so Locust isn't the bottleneck |

**Request mix:** 80% GET existing, 15% POST valid details, 3% GET missing (expects 404), 2% POST invalid (expects 400). The expected 404s and 400s are marked as successes so the failure rate only counts real problems.

### Experiments

| Run | Client | Workers | Users / spawn | Wait | Purpose |
|---|---|---|---|---|---|
| 1 | HttpUser | 1 | 50 / 10 | normal | Baseline (same as HW3) |
| 2 | FastHttpUser | 1 | 50 / 10 | normal | Client comparison at the same offered load |
| 3 | HttpUser | 1 | 200 / 20 | none | Client comparison when the load generator is saturated |
| 4 | FastHttpUser | 1 | 200 / 20 | none | Same |
| 5 | FastHttpUser | 4 | step shape to 1000 | none | Stress test: find the breaking point |

### Results

*(Fill in from your runs; screenshots are in `screenshots/locust/`.)*

| Run | RPS | Failures | p50 | p95 | p99 | Worker CPU |
|---|---|---|---|---|---|---|
| 1 | | | | | | |
| 2 | | | | | | |
| 3 | | | | | | |
| 4 | | | | | | |
| 5 (peak) | | | | | | |

**Where the server started to struggle:** *(For example: "Throughput stopped rising at about N RPS around U users while p95 climbed from X ms to Y ms. The task has 0.25 vCPU, so CPU in the ECS metrics tab hit 100% at the same moment.")*

### Tradeoffs

**Which operations are most common, and what does that mean for the data structure?** In a real store, people browse far more than sellers edit listings, so reads (GET) dominate, often 90%+ of traffic. That suits a hash map: lookup by ID is O(1) on average and doesn't slow down as the catalog grows. It also suits a `sync.RWMutex`, because any number of GETs can hold the read lock at the same time, and only the rarer POSTs need exclusive access. If writes became common, one global lock would become the bottleneck, and the next step would be sharding the map (N maps, each with its own lock, chosen by `id % N`) or using `sync.Map`, which is optimized for read-mostly keys. What the map can't do is answer queries other than "by ID" (for example, all products in category 5) without scanning everything, which is why a real store would add a database with indexes.

**HttpUser vs FastHttpUser.** *(Summarize your runs 1–4.)* Expected pattern, consistent with HW3: at the same think time both clients produce the same RPS, because think time fixes how often users send requests, but FastHttpUser uses noticeably less worker CPU. With no think time, the worker's CPU becomes the limit, and FastHttpUser produces more RPS from the same worker. The reason is per-request client cost. HttpUser uses python-requests, which is pure Python and creates sessions, prepared requests and response objects for every call. FastHttpUser uses geventhttpclient, whose HTTP parsing is written in C and which was built for gevent's greenlets, the lightweight user-level threads Locust runs each simulated user on. Because the GIL limits a Python process to roughly one core, a cheaper client means one worker can simulate more users before it, rather than the server, becomes the bottleneck.

## Part V: Observability

**RED signals tracked for every endpoint:**

| Signal | Metric | Grafana query (per route) |
|---|---|---|
| Rate | `http_requests_total` (counter) | `sum by (method, route) (rate(http_requests_total[30s]))` |
| Errors | `http_request_errors_total` (counter, non-2xx) | errors ÷ total × 100 |
| Duration | `http_request_duration_seconds` (histogram) | `histogram_quantile(0.95, sum by (le, route) (rate(..._bucket[30s])))` |

Metrics are labelled with the **route template** (`/products/:productId`), not the actual path, so 1000 product IDs don't create 1000 separate time series.

**Run the stack:**

```bash
cd observability
docker compose up --build
# API http://localhost:8080 · Prometheus http://localhost:9090 · Grafana http://localhost:3000
```

Grafana opens directly into the provisioned **Product API – RED signals** dashboard (Rate, Errors %, Errors by status, p50/p95/p99 Duration, and goroutines/heap). Point Locust at `http://host.docker.internal:8080` to generate load.

**Dashboard: idle vs loaded.** *(Screenshots in `screenshots/grafana/`.)*

**Request log excerpt** from the same run (one JSON line per request, on stdout, and in CloudWatch on ECS):

```json
{"time":"2026-10-19T10:41:07.512Z","level":"INFO","msg":"request","method":"GET","endpoint":"/products/:productId","path":"/products/412","status":200,"duration_ms":0.084,"bytes":118,"client_ip":"172.18.0.1"}
{"time":"2026-10-19T10:41:07.513Z","level":"INFO","msg":"request","method":"POST","endpoint":"/products/:productId/details","path":"/products/77/details","status":204,"duration_ms":0.131,"bytes":-1,"client_ip":"172.18.0.1"}
{"time":"2026-10-19T10:41:07.515Z","level":"INFO","msg":"request","method":"GET","endpoint":"/products/:productId","path":"/products/5312","status":404,"duration_ms":0.061,"bytes":97,"client_ip":"172.18.0.1"}
```
*(Replace with lines copied from `docker compose logs product-service` during your Locust run.)*

**Dashboard vs logs:** I'd look at the dashboard to notice and scope a problem while it's happening: is traffic up, are errors spiking, which endpoint's p95 moved, and when. I'd look at the logs afterwards to explain a specific problem: which exact requests failed, with which paths and status codes, at what time, especially when nobody had Grafana open when it happened.

The design questions (scalable backend for the full `api.yaml`, declarative vs imperative Terraform) are answered in [`docs/INTERVIEW_PREP.md`](docs/INTERVIEW_PREP.md).
