class KimiProxy < Formula
  desc "Encrypted KCP tunnel exposing kimi web through a public server"
  homepage "https://github.com/syhily/kimi-proxy"
  url "https://github.com/syhily/kimi-proxy/archive/refs/tags/v0.1.1.tar.gz"
  sha256 "a74a102d90beb7264b24204f6c2549b3e523d9d3971cef608b65e396e5c3bf24"
  head "https://github.com/syhily/kimi-proxy.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w", output: bin/"kimi-proxy-client"), "./cmd/client"
    system "go", "build", *std_go_args(ldflags: "-s -w", output: bin/"kimi-proxy-server"), "./cmd/server"
    # Install a default config on first install only; never overwrite user edits.
    (etc/"kimi-proxy").install "config.example.json" => "config.json" unless (etc/"kimi-proxy/config.json").exist?
  end

  service do
    run [opt_bin/"kimi-proxy-client", "-config", etc/"kimi-proxy/config.json"]
    keep_alive true
    environment_variables PATH: std_service_path_env
    log_path var/"log/kimi-proxy.log"
    error_log_path var/"log/kimi-proxy.error.log"
  end

  def caveats
    <<~EOS
      The repository is private. Before installing or upgrading, export a
      GitHub token so Homebrew can download the source tarball:
        export HOMEBREW_GITHUB_API_TOKEN=$(gh auth token)

      Edit the config file with your server address and token first:
        #{etc}/kimi-proxy/config.json

      The client spawns `kimi web`; make sure the kimi CLI is installed and
      logged in. Under `brew services` the PATH is minimal, so set an absolute
      "kimi_bin" path in the config if kimi is not installed in a standard
      location.

      Start the client as a background service:
        brew services start kimi-proxy
    EOS
  end

  test do
    assert_match "server is required", shell_output("#{bin}/kimi-proxy-client 2>&1", 1)
    assert_match "token is required", shell_output("#{bin}/kimi-proxy-server 2>&1", 1)
  end
end
