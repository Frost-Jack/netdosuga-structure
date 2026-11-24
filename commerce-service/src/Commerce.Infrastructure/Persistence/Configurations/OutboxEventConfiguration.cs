using Commerce.Domain.Entities;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Commerce.Infrastructure.Persistence.Configurations;

internal sealed class OutboxEventConfiguration : IEntityTypeConfiguration<OutboxEvent>
{
    public void Configure(EntityTypeBuilder<OutboxEvent> builder)
    {
        builder.ToTable("outbox_events");
        builder.HasKey(x => x.EventId);
        builder.Property(x => x.Payload).IsRequired();
        builder.Property(x => x.Headers).IsRequired();
        builder.HasIndex(x => x.PublishedAt);
    }
}
