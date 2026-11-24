using Commerce.Domain;
using Commerce.Domain.Entities;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Commerce.Infrastructure.Persistence.Configurations;

internal sealed class TicketConfiguration : IEntityTypeConfiguration<Ticket>
{
    public void Configure(EntityTypeBuilder<Ticket> builder)
    {
        builder.ToTable("tickets");
        builder.HasKey(x => x.Code);
        builder.Property(x => x.Status)
            .HasConversion(v => v.ToString().ToUpperInvariant(), v => Enum.Parse<TicketStatus>(v, true))
            .HasMaxLength(16);
        builder.Property(x => x.Code).HasMaxLength(128).IsRequired();
        builder.Property(x => x.OrderId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.OfferId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.VendorId).HasMaxLength(128).IsRequired();
        builder.HasIndex(x => x.OrderId);
        builder.HasOne<Order>().WithMany().HasForeignKey(x => x.OrderId).OnDelete(DeleteBehavior.Cascade);
    }
}
