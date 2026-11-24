using System.Text.Json;
using Commerce.Domain.Ports;
using Commerce.Infrastructure.Persistence;
using Confluent.Kafka;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Api.Background;

public sealed class OutboxPublisher(
    IServiceScopeFactory scopes,
    IConfiguration configuration,
    ILogger<OutboxPublisher> logger) : BackgroundService
{
    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var brokers = configuration["KAFKA_BROKERS"] ?? "kafka:9092";
        var topic = configuration["BUSINESS_KAFKA_TOPIC"] ?? "netdosuga.business.events.v1";
        using var producer = new ProducerBuilder<string, string>(new ProducerConfig
        {
            BootstrapServers = brokers,
            Acks = Acks.All,
            EnableIdempotence = true,
            MessageTimeoutMs = 3000
        }).Build();

        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                using var scope = scopes.CreateScope();
                var outbox = scope.ServiceProvider.GetRequiredService<IOutboxRepository>();
                var db = scope.ServiceProvider.GetRequiredService<CommerceDbContext>();
                var events = await outbox.ListUnpublishedAsync(20, stoppingToken);
                foreach (var @event in events)
                {
                    var message = new Message<string, string> { Key = @event.EventId, Value = @event.Payload };
                    message.Headers ??= new Headers();
                    using var headers = JsonDocument.Parse(@event.Headers);
                    foreach (var header in headers.RootElement.EnumerateObject())
                    {
                        message.Headers.Add(header.Name, System.Text.Encoding.UTF8.GetBytes(header.Value.GetString() ?? string.Empty));
                    }

                    await producer.ProduceAsync(topic, message, stoppingToken);
                    await outbox.MarkPublishedAsync(@event.EventId, stoppingToken);
                }

                await db.SaveChangesAsync(stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception error)
            {
                logger.LogWarning(error, "Outbox publish retry");
            }

            await Task.Delay(500, stoppingToken);
        }
    }
}
