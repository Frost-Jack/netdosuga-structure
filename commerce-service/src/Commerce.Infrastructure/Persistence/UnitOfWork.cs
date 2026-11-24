using System.Diagnostics;
using Commerce.Domain.Ports;
using Commerce.Infrastructure.Observability;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence;

public sealed class UnitOfWork(CommerceDbContext db) : IUnitOfWork
{
    public async Task<T> ExecuteAsync<T>(Func<CancellationToken, Task<T>> action, string? customerId = null, CancellationToken cancellationToken = default)
    {
        using var activity = InfrastructureTelemetry.Source.StartActivity("db.SELECT", ActivityKind.Client);
        await using var transaction = await db.Database.BeginTransactionAsync(cancellationToken);
        try
        {
            if (!string.IsNullOrEmpty(customerId))
            {
                await db.Database.ExecuteSqlRawAsync(
                    "SELECT pg_advisory_xact_lock(hashtext({0}))",
                    [customerId],
                    cancellationToken);
            }

            var result = await action(cancellationToken);
            await db.SaveChangesAsync(cancellationToken);
            using var commit = InfrastructureTelemetry.Source.StartActivity("transaction.commit", ActivityKind.Client);
            await transaction.CommitAsync(cancellationToken);
            return result;
        }
        catch
        {
            await transaction.RollbackAsync(cancellationToken);
            throw;
        }
    }

    public Task FlushAsync(CancellationToken cancellationToken = default) => db.SaveChangesAsync(cancellationToken);
}
