using Commerce.Application.Handlers;

namespace Commerce.Api.Background;

public sealed class ReservationExpiryWorker(
    IServiceScopeFactory scopes,
    ILogger<ReservationExpiryWorker> logger) : BackgroundService
{
    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                using var scope = scopes.CreateScope();
                var service = scope.ServiceProvider.GetRequiredService<ReservationExpiryService>();
                await service.ExpireOnceAsync(stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception error)
            {
                logger.LogWarning(error, "Reservation expiry retry");
            }

            await Task.Delay(500, stoppingToken);
        }
    }
}
