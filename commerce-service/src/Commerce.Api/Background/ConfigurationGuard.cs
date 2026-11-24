namespace Commerce.Api.Background;

public sealed class ConfigurationGuard(IConfiguration configuration) : IHostedService
{
    public Task StartAsync(CancellationToken cancellationToken)
    {
        if (configuration["APP_ENV"] != "local")
        {
            throw new InvalidOperationException("APP_ENV must be local.");
        }

        if (configuration["MOCK_MODE"] != "true")
        {
            throw new InvalidOperationException("MOCK_MODE must be true.");
        }

        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
}
