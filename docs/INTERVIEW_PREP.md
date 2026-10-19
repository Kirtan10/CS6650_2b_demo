# HW5 Mock Interview Prep

## Part II: The API and the server

### Walking through the Product section of api.yaml

- **`paths`** lists the endpoints. Each path has one or more operations (`get`, `post`), and each operation has `parameters`, an optional `requestBody`, and `responses`.
- **`GET /products/{productId}`**: `productId` is a *path parameter* (part of the URL), required, `type: integer`, `format: int32`, `minimum: 1`. Responses: **200** returns a `Product`; **404** means no product has that ID; **500** means the server failed. There is **no 400** for GET, so an ID like `abc` or `0` returns 404: it can't identify any product.
- **`POST /products/{productId}/details`**: "add or update detailed information for a specific product." Same path parameter, plus a required JSON `requestBody` that must match the `Product` schema. **204** means success with no response body; **400** means the input was invalid; **404** means the product doesn't exist; **500** means the server failed.
- **`components/schemas`** holds reusable shapes, referenced with `$ref`. **`Product`** lists `required` fields and constraints (`minimum`, `minLength`, `maxLength`). **`Error`** is the shape of every error body: `error` (a machine-readable code), `message` (human-readable), and an optional `details`.
- **`format: int32`** on every integer means values must fit in 32 bits (max 2,147,483,647). Go's `int` is 64-bit, so the server checks this explicitly.
- **`securitySchemes`** (API key, Bearer JWT) are declared globally, but no 401 is defined for these endpoints, so auth isn't part of this assignment.
- **Why a contract matters:** the frontend, the other services and the tests can all be written against the yaml before the server exists, as long as the server implements it exactly.

### How a request moves through product-service.go

Take `POST /products/7/details`:

1. **`http.Server`** accepts the TCP connection and gives the request to Gin's engine. Go runs each request on its own goroutine.
2. **Gin's router** matches the method and path against a radix tree of routes, finds `/products/:productId/details`, and stores `productId = "7"`.
3. **Middleware chain, in order:**
   - `observe()` records the start time and calls `c.Next()`.
   - `gin.CustomRecovery` wraps the rest in a `recover()`, so a panic becomes a 500 instead of crashing the server.
   - `faultInjection()` panics only if enabled and the header is present.
4. **The handler `addProductDetails`:** `parseProductID` turns `"7"` into an int32-range int (or the handler returns 400) → the JSON body is decoded into `productInput` (pointer fields, so a missing field is `nil`) → `validateProduct` checks required fields and limits (400) → the body ID is compared to the path ID (400) → `store.UpdateDetails` takes the write lock and updates the map, or returns false (404) → `c.Status(204)`.
5. **Back out through the middleware:** control returns to `observe()`, which reads the final status and duration, updates the three Prometheus metrics, and writes one JSON log line.
6. Gin writes the response headers, and the client receives `204 No Content`.

A GET is the same, except the handler calls `store.Get` under the read lock and returns `c.JSON(200, product)`.

### Why a map, and what breaks under concurrency

- **Why a map:** every operation is "look up by product ID," and a hash map does that in O(1) average time regardless of catalog size. A slice would need an O(n) scan, as the albums server did.
- **What would break without the lock:** Gin serves requests on many goroutines at once, and Go maps are not safe for concurrent writes. Two POSTs at the same time can corrupt the map's internals, and Go detects this and crashes the whole process with `fatal error: concurrent map writes`. A read during a write is also a data race. This is the HW3 lesson.
- **Why `RWMutex` rather than `Mutex`:** reads are about 80% or more of traffic. A read lock can be held by many goroutines simultaneously, so GETs don't block each other; only POSTs wait for exclusive access.
- **What still limits it:** with one global lock, every write blocks every read for its duration. Under a write-heavy load, requests queue on the lock (Amdahl's serial fraction). The fixes are sharding (N maps with N locks) or `sync.Map`, and beyond one machine, an external store.
- **Other limits:** data lives only in this process's memory, so it's lost on restart, and two ECS tasks would each have their own separate copy. That's why a real system uses a shared database.

### How each response code was triggered

| Code | Trigger |
|---|---|
| GET 200 | `/products/1` (seeded) |
| GET 404 | `/products/999999`, and invalid IDs `/products/abc`, `/products/0` |
| POST 204 | valid body with matching ID |
| POST 400 | invalid path ID, malformed JSON, missing fields, out-of-range values (including > int32), wrong types, body/path ID mismatch |
| POST 404 | valid body for ID 999999 |
| 500 (both) | `ENABLE_FAULT_INJECTION=true` + header `X-Fault-Injection: 500` → panic → recovery returns 500 |

**Why fault injection instead of a real bug:** a correct server shouldn't produce 500s on purpose, but you still need to prove the 500 path returns the right `Error` body and is counted in metrics. Gating it on an env var plus a header means it can't happen in production by accident.

## Part III: Terraform

- **`provider.tf`** configures the plugins Terraform uses to talk to APIs: the AWS provider (region, credentials from `~/.aws/credentials`) and the Docker provider (builds and pushes the image, authenticated to ECR).
- **`variables.tf`** holds inputs that may change between environments: region, service name, `container_port` (8080), CPU and memory. Changing a value here changes the deployment without editing resources.
- **`modules/`** groups related resources into reusable units, for example ECR (the image repository), ECS (cluster, task definition, service), networking (security group, subnets), and logging (CloudWatch log group). Each module has its own inputs and outputs, like a function.
- **`main.tf`** calls the modules and wires them together: the ECR module's repository URL feeds the image build, the image feeds the ECS task definition, and the security group and log group feed the ECS service.
- **`output.tf`** prints values you need afterwards, such as the cluster and service names and the ECR URL.
- **How they connect:** `terraform init` downloads the providers; `plan` builds a dependency graph from the references between resources and compares it to the state file; `apply` creates things in dependency order (repo → image → task definition → service) and records them in `terraform.tfstate`.
- **What I changed:** the server code and Dockerfile in `src/`, the service name variables, and the `linux/amd64` platform for Apple Silicon. The Terraform structure itself didn't need to change, because the Dockerfile stayed at the same path and the port stayed 8080.

## Part IV: Testing

- **Test design:** request mix 80/15/3/2 to model browse-heavy shopping, including some expected 404s and 400s so error paths are exercised under load. Spawn rate is 10–20 users/s, so the ramp is visible on the charts. The step shape adds 100 users every 60 s, so each step reaches a steady state and the breaking point is easy to read. Workers are scaled to 4, so Locust itself isn't the bottleneck.
- **Breaking point:** look for the step where RPS stops increasing while p95 and p99 rise sharply. That's queueing: requests arrive faster than the 0.25 vCPU task can process them, so they wait. Check ECS CPU utilization to confirm the cause.
- **HttpUser vs FastHttpUser:** see the README. The short version: same RPS when think time caps the load, less client CPU with FastHttpUser, and more RPS from FastHttpUser only when the worker is CPU-bound.

## Part V: Observability and design questions

### Showing the dashboard
Idle: Rate near 0, Errors flat (or empty), Duration noisy because there are few samples. Loaded: Rate jumps to the Locust RPS, with GET about 5× POST because of the mix; Errors around 5% because of the deliberate 404s and 400s (they're non-2xx, so they count); p95 rises as users increase.

### How would you design a scalable backend for the whole api.yaml?

Besides products, the spec defines four more groups: shopping carts (`POST /shopping-carts`, `/{id}/items`, `/{id}/checkout`), the warehouse (`/warehouse/reserve`, `/warehouse/ship`), and payments (`/payments/checkout`, which can return **402 Payment Declined**). The `400` on reserve and ship for "insufficient inventory" tells you inventory must be counted exactly. A scalable design:

- **Entry:** an Application Load Balancer (or API Gateway) in front, routing by path to separate services.
- **Split into services by domain:** Product, Cart, Warehouse/Inventory, and Payment, each deployed independently on ECS with auto scaling, so the read-heavy product catalog can scale without scaling payments.
- **Stateless services, state in databases:** move data out of process memory so any task can serve any request.
  - Products: a database such as DynamoDB keyed by `product_id`, or Postgres if you need queries by category, plus a cache (ElastiCache/Redis, or a CDN for product pages) because reads dominate.
  - Carts: a key-value store keyed by cart ID, with a TTL for abandoned carts.
  - Inventory and orders: a relational database with transactions, because reserving stock must not oversell.
- **Checkout as a workflow, not one synchronous call:** checkout reserves inventory, charges payment, and then confirms. Use a queue (SQS) or events (SNS/Kafka) between services, with idempotency keys so retries don't double-charge, and compensating actions (release the reservation) if payment fails, which is the saga pattern.
- **Resilience:** timeouts, retries with backoff, and circuit breakers around the payment provider. Run tasks across multiple Availability Zones.
- **Observability:** the same RED metrics per service, plus distributed tracing (a request ID passed through every service) so one slow checkout can be followed across services.

### "Terraform is declarative." What does that mean?

- **Declarative:** you describe *what the end state should be* ("one ECR repo, one ECS service running 1 task of this image on port 8080"), and Terraform works out *how* to get there. It compares your configuration to the recorded state and the real infrastructure, then creates, updates, or deletes only the differences.
- **Imperative:** you write the *steps* ("create repo; build image; push; create cluster; register task definition; run service"), like the console clicks or AWS CLI commands from HW4. Running the same script twice tries to create everything twice and fails or makes duplicates.
- **Why it helps:**
  - **Idempotent:** running `apply` again with no changes does nothing.
  - **Reproducible:** anyone in the group can recreate the same system from the repo, which is the README's goal.
  - **Reviewable:** `plan` shows changes before they happen, and infrastructure changes go through Git like code.
  - **Dependencies handled automatically:** Terraform orders resource creation from the references between them.
  - **Clean teardown:** `destroy` removes exactly what was created, which matters with a limited lab budget.
