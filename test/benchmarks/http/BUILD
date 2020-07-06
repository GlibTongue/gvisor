load("//tools:defs.bzl", "go_library", "go_test")

package(licenses = ["notice"])

go_library(
    name = "http",
    srcs = [
        "http.go",
    ],
)

go_test(
    name = "http_test",
    size = "large",
    srcs = [
        "httpd_test.go",
    ],
    library = ":http",
    tags = [
        # Requires docker and runsc to be configured before test runs.
        "manual",
        "local",
    ],
    visibility = ["//:sandbox"],
    deps = [
        "//pkg/test/dockerutil",
        "//test/benchmarks/harness",
    ],
)
