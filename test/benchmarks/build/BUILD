load("//tools:defs.bzl", "go_library", "go_test")

package(licenses = ["notice"])

go_library(
    name = "build",
    testonly = 1,
    srcs = ["build.go"],
)

go_test(
    name = "build_test",
    size = "large",
    srcs = [
        "bazel_test.go",
    ],
    library = ":build",
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
