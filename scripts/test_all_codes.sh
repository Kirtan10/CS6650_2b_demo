#!/usr/bin/env bash
# Sends one request per response code defined for the Product endpoints.
# Usage: ./scripts/test_all_codes.sh [BASE_URL]
#   local:  ./scripts/test_all_codes.sh http://localhost:8080
#   ECS:    ./scripts/test_all_codes.sh http://<TASK_PUBLIC_IP>:8080
# The 500 case only works when the server runs with ENABLE_FAULT_INJECTION=true.
BASE=${1:-http://localhost:8080}
GOOD='{"product_id":1,"sku":"ABC-123-XYZ","manufacturer":"Acme Corporation","category_id":456,"weight":1250,"some_other_id":789}'

run() {  # run "<label>" <curl args...>
  local label=$1; shift
  echo "=== $label"
  echo "\$ curl $*"
  curl -s -w '\n--> HTTP %{http_code}\n\n' "$@"
}

echo "##### GET /products/{productId}"
run "200 OK: existing product"              "$BASE/products/1"
run "404: product does not exist"           "$BASE/products/999999"
run "404: productId not an integer"         "$BASE/products/abc"
run "404: productId below minimum (0)"      "$BASE/products/0"
run "500: injected server fault"            -H 'X-Fault-Injection: 500' "$BASE/products/1"

echo "##### POST /products/{productId}/details"
run "204 No Content: details updated"       -X POST -H 'Content-Type: application/json' -d "$GOOD" "$BASE/products/1/details"
run "  (verify with GET: new details)"      "$BASE/products/1"
run "400: malformed JSON"                   -X POST -H 'Content-Type: application/json' -d '{"product_id":1,' "$BASE/products/1/details"
run "400: missing required fields"          -X POST -H 'Content-Type: application/json' -d '{"product_id":1,"sku":"X"}' "$BASE/products/1/details"
run "400: constraint violations"            -X POST -H 'Content-Type: application/json' -d '{"product_id":1,"sku":"","manufacturer":"Acme","category_id":0,"weight":-5,"some_other_id":1}' "$BASE/products/1/details"
run "400: wrong type (weight is a string)"  -X POST -H 'Content-Type: application/json' -d '{"product_id":1,"sku":"A","manufacturer":"Acme","category_id":1,"weight":"heavy","some_other_id":1}' "$BASE/products/1/details"
run "400: invalid path productId"          -X POST -H 'Content-Type: application/json' -d "$GOOD" "$BASE/products/abc/details"
run "400: value too big for int32"          -X POST -H 'Content-Type: application/json' -d '{"product_id":1,"sku":"A","manufacturer":"Acme","category_id":3000000000,"weight":1,"some_other_id":1}' "$BASE/products/1/details"
run "400: body id != path id"               -X POST -H 'Content-Type: application/json' -d "$GOOD" "$BASE/products/2/details"
run "404: product does not exist"           -X POST -H 'Content-Type: application/json' -d "${GOOD/\"product_id\":1/\"product_id\":999999}" "$BASE/products/999999/details"
run "500: injected server fault"            -X POST -H 'X-Fault-Injection: 500' -H 'Content-Type: application/json' -d "$GOOD" "$BASE/products/1/details"
