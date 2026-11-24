using Commerce.Infrastructure.Persistence;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Api.Background;

public sealed class StartupMigrationService(
    IServiceScopeFactory scopes,
    ILogger<StartupMigrationService> logger) : IHostedService
{
    public async Task StartAsync(CancellationToken cancellationToken)
    {
        for (var attempt = 1; attempt <= 10; attempt++)
        {
            try
            {
                using var scope = scopes.CreateScope();
                var db = scope.ServiceProvider.GetRequiredService<CommerceDbContext>();
                await db.Database.MigrateAsync(cancellationToken);
                return;
            }
            catch (Exception error) when (attempt < 10)
            {
                logger.LogWarning(error, "Database migration attempt {Attempt} failed", attempt);
                await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken);
            }
        }
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
}
